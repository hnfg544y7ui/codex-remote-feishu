package codex

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAsyncQuestionCrossClientNativeIdentity(t *testing.T) {
	got := extractAsyncQuestions("agent_message", "call-native", map[string]any{
		"delivery": "async", "questions": []any{map[string]any{"title": "Choose"}},
	})
	if len(got) != 1 || got[0].ID != `["request_user_input_async","call-native",0]` {
		t.Fatalf("question identity differs from native desktop reply: %#v", got)
	}
}

func TestAsyncQuestionCrossClientRejectsAmbiguousReplies(t *testing.T) {
	valid := `[{"questionItemId":"q","answer":"A"}]`
	wrap := func(body string) string {
		return "<send_user_message_question_reply>" + body + "</send_user_message_question_reply>"
	}
	for name, text := range map[string]string{
		"ordinary answer": "A", "quoted envelope": "Example: " + wrap(valid),
		"empty": wrap(`[]`), "malformed": wrap(`[`), "null": wrap(`null`),
		"duplicate identity":    wrap(`[{"questionItemId":"q","answer":"A"},{"questionItemId":"q","answer":"B"}]`),
		"partial invalid batch": wrap(`[{"questionItemId":"q","answer":"A"},{"questionItemId":"x","answer":false}]`),
		"unknown field":         wrap(`[{"questionItemId":"q","answer":"A","unexpected":true}]`),
		"empty answer":          wrap(`[{"questionItemId":"q","answer":" "}]`),
		"trailing value":        wrap(valid + ` {}`),
		"oversize":              wrap(`[{"questionItemId":"q","answer":"` + strings.Repeat("a", 5121) + `"}]`),
	} {
		t.Run(name, func(t *testing.T) {
			if got := extractAsyncQuestionAnswers("user_message", map[string]any{"text": text}); len(got) != 0 {
				t.Fatalf("malformed answer manufactured a resolution: %#v", got)
			}
		})
	}
	if got := extractAsyncQuestionAnswers("agent_message", map[string]any{"text": wrap(valid)}); len(got) != 0 {
		t.Fatal("assistant/tool output must not resolve user questions")
	}
}

func TestAsyncQuestionCrossClientReplyTranslation(t *testing.T) {
	for _, method := range []string{"item/started", "item/completed"} {
		tr := NewTranslator("inst")
		item := map[string]any{"type": "userMessage", "id": "reply", "content": []any{
			map[string]any{"type": "text", "text": "<send_user_message_question_reply>\n[{\"questionItemId\":\"native-q\",\"question\":\"Choose\",\"answer\":\"A\"}]\n</send_user_message_question_reply>"},
		}}
		frame, _ := json.Marshal(map[string]any{"method": method, "params": map[string]any{"threadId": "thread", "turnId": "later-turn", "item": item}})
		result, err := tr.ObserveServer(frame)
		if err != nil || len(result.Events) != 1 {
			t.Fatalf("unexpected translation: %#v %v", result, err)
		}
		encoded, _ := json.Marshal(result.Events[0])
		var wire map[string]any
		_ = json.Unmarshal(encoded, &wire)
		answers, _ := wire["asyncQuestionAnswers"].([]any)
		if len(answers) != 1 {
			t.Fatalf("native structured answer was not observed: %s", encoded)
		}
		if len(result.OutboundToCodex) != 0 || len(result.OutboundToParent) != 0 {
			t.Fatal("observing an answer must not send it again")
		}
	}
}

func TestAsyncQuestionCrossClientSharedReplyStaysInSubscribedThread(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "thread")
	text := `<send_user_message_question_reply>[{"questionItemId":"q","answer":"A"}]</send_user_message_question_reply>`
	for _, thread := range []string{"other", "thread"} {
		frame, _ := json.Marshal(map[string]any{"method": "item/completed", "params": map[string]any{
			"threadId": thread, "turnId": "desktop-turn", "item": map[string]any{"id": "reply", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": text}}},
		}})
		result, err := tr.ObserveServer(frame)
		if err != nil {
			t.Fatal(err)
		}
		if thread == "other" {
			if len(result.Events) != 0 {
				t.Fatal("another thread's answer leaked into this instance")
			}
		} else if len(result.Events) != 1 || len(result.Events[0].AsyncQuestionAnswers) != 1 {
			t.Fatal("shared desktop reply was lost", result)
		}
		if len(result.OutboundToCodex) != 0 || len(result.OutboundToParent) != 0 {
			t.Fatal("observing desktop reply sent data")
		}
	}
}
