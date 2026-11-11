package codex

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestAsyncQuestionNotificationPreservesDeliveryWithoutCreatingRPCRequest(t *testing.T) {
	tr := NewTranslator("inst")
	result, err := tr.ObserveServer([]byte(`{"method":"item/completed","params":{"threadId":"thread","turnId":"turn","item":{"id":"call-question","type":"agentMessage","text":"是否允许升级？","phase":"final_answer","delivery":"async","questions":[{"title":"是否允许升级？","options":["允许","暂不"]}]}}}`))
	if err != nil || len(result.Events) != 1 {
		t.Fatalf("unexpected result: %+v %v", result, err)
	}
	event := result.Events[0]
	if event.Kind != agentproto.EventItemCompleted || event.RequestID != "" || event.RequestPrompt != nil || event.Metadata["messageDelivery"] != "async" || event.Metadata["hasAsyncQuestions"] != true {
		t.Fatalf("async question was lost or converted to an RPC request: %+v", event)
	}
	want := []agentproto.AsyncQuestion{{ID: "async:call-question:0", Title: "是否允许升级？", Options: []string{"允许", "暂不"}}}
	if !reflect.DeepEqual(event.AsyncQuestions, want) {
		t.Fatalf("typed questions lost their source identity: got %#v, want %#v", event.AsyncQuestions, want)
	}
}

func asyncQuestionFrameForTest(t *testing.T, method, itemID, delivery string, questions any) []byte {
	t.Helper()
	frame, err := json.Marshal(map[string]any{"method": method, "params": map[string]any{
		"threadId": "thread", "turnId": "turn", "item": map[string]any{
			"id": itemID, "type": "agentMessage", "delivery": delivery, "text": "needs an answer", "questions": questions,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func TestAsyncQuestionTypedIdentitySurvivesStartedCompletedAndJSON(t *testing.T) {
	questions := []any{
		map[string]any{"id": "opaque question id", "title": "First", "options": []any{"One", "Two"}},
		map[string]any{"questionItemId": `["request_user_input_async","call-a",1]`, "title": "Second"},
		map[string]any{"title": "Third", "options": []any{}},
	}
	want := []agentproto.AsyncQuestion{
		{ID: "opaque question id", Title: "First", Options: []string{"One", "Two"}},
		{ID: `["request_user_input_async","call-a",1]`, Title: "Second"},
		{ID: "async:source-item:2", Title: "Third"},
	}
	tr := NewTranslator("inst")
	tr.currentThreadID = "selected-before-notification"
	for _, method := range []string{"item/started", "item/completed", "item/completed"} {
		result, err := tr.ObserveServer(asyncQuestionFrameForTest(t, method, "source-item", "async", questions))
		if err != nil || len(result.Events) != 1 {
			t.Fatalf("unexpected translation for %s: %#v, %v", method, result, err)
		}
		event := result.Events[0]
		if !reflect.DeepEqual(event.AsyncQuestions, want) {
			t.Fatalf("unstable question identity for %s: %#v", method, event.AsyncQuestions)
		}
		if event.RequestID != "" || event.RequestPrompt != nil || event.CommandID != "" || tr.currentThreadID != "selected-before-notification" ||
			len(result.OutboundToCodex) != 0 || len(result.OutboundToParent) != 0 {
			t.Fatal("async notification manufactured an RPC request, action or selection", result)
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var roundtrip agentproto.Event
		if err := json.Unmarshal(encoded, &roundtrip); err != nil || !reflect.DeepEqual(roundtrip.AsyncQuestions, want) {
			t.Fatal("agent wire roundtrip lost questions", string(encoded), err)
		}
	}
}

func TestAsyncQuestionMalformedOrOversizeListFallsBackWithoutPartialQuestions(t *testing.T) {
	valid := func() map[string]any { return map[string]any{"title": "valid", "options": []any{"one"}} }
	cases := map[string]any{
		"malformed later question": []any{valid(), "not an object"},
		"empty later title":        []any{valid(), map[string]any{"title": " "}},
		"non-string title":         []any{map[string]any{"title": 1}},
		"oversize title":           []any{map[string]any{"title": strings.Repeat("x", maxAsyncQuestionTitle+1)}},
		"non-list options":         []any{map[string]any{"title": "q", "options": "one"}},
		"invalid later option":     []any{map[string]any{"title": "q", "options": []any{"one", false}}},
		"empty option":             []any{map[string]any{"title": "q", "options": []any{" "}}},
		"oversize option":          []any{map[string]any{"title": "q", "options": []any{strings.Repeat("x", maxAsyncOptionText+1)}}},
		"non-string id":            []any{map[string]any{"title": "q", "id": 42}},
		"oversize id":              []any{map[string]any{"title": "q", "id": strings.Repeat("x", maxAsyncQuestionID+1)}},
		"conflicting aliases":      []any{map[string]any{"title": "q", "id": "one", "questionItemId": "two"}},
		"duplicate question id":    []any{map[string]any{"title": "one", "id": "same"}, map[string]any{"title": "two", "id": "same"}},
	}
	tooMany := make([]any, maxAsyncQuestions+1)
	for i := range tooMany {
		tooMany[i] = valid()
	}
	cases["too many questions"] = tooMany
	tooManyOptions := make([]any, maxAsyncOptions+1)
	for i := range tooManyOptions {
		tooManyOptions[i] = "option"
	}
	cases["too many options"] = []any{map[string]any{"title": "q", "options": tooManyOptions}}
	for name, questions := range cases {
		t.Run(name, func(t *testing.T) {
			tr := NewTranslator("inst")
			result, err := tr.ObserveServer(asyncQuestionFrameForTest(t, "item/completed", "source-item", "async", questions))
			if err != nil || len(result.Events) != 1 {
				t.Fatalf("malformed question must retain legacy notification: %#v, %v", result, err)
			}
			event := result.Events[0]
			if len(event.AsyncQuestions) != 0 || event.Metadata["hasAsyncQuestions"] != true || event.Metadata["text"] != "needs an answer" || event.RequestID != "" || event.RequestPrompt != nil {
				t.Fatal("malformed list became partial questions or lost legacy fallback", event)
			}
		})
	}
}

func TestAsyncQuestionTypedPayloadRequiresAsyncAgentMessageAndStableIdentity(t *testing.T) {
	questions := []any{map[string]any{"title": "question"}}
	for _, delivery := range []string{"", "sync"} {
		tr := NewTranslator("inst")
		result, err := tr.ObserveServer(asyncQuestionFrameForTest(t, "item/completed", "item", delivery, questions))
		if err != nil || len(result.Events) != 1 || len(result.Events[0].AsyncQuestions) != 0 {
			t.Fatal("ordinary agent message became an async answer request", result, err)
		}
	}
	if got := extractAsyncQuestions("agent_message", "", map[string]any{"delivery": "async", "questions": questions}); len(got) != 0 {
		t.Fatal("missing native item identity received a generated question id", got)
	}
	if got := extractAsyncQuestions("mcp_tool_call", "item", map[string]any{"delivery": "async", "questions": questions}); len(got) != 0 {
		t.Fatal("non-agent item became an async answer request", got)
	}
}
