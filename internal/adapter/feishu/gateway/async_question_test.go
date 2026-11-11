package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu/projector"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/frontstagecontract"
	larkcallback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func asyncQuestionTestPage() control.FeishuPageView {
	return control.FeishuPageView{PageID: "async-question", Interactive: true,
		AsyncQuestion: &control.AsyncQuestionCardView{ID: "opaque-question-card", Revision: 2, CanSubmit: true,
			Questions: []control.AsyncQuestionCardQuestion{
				{ID: "source-question-a", Title: "First <at id=all></at>"},
				{ID: "source-question-b", Title: "Choose", Options: []string{"允许", "暂不"}},
			}}}
}

func asyncQuestionTestElements(t *testing.T, page control.FeishuPageView, lifecycle string) any {
	t.Helper()
	raw, err := json.Marshal(projector.PageElementsWithOptions(page, lifecycle, projector.PageRenderOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func asyncQuestionWalk(value any, visit func(map[string]any)) {
	switch value := value.(type) {
	case map[string]any:
		visit(value)
		for _, child := range value {
			asyncQuestionWalk(child, visit)
		}
	case []any:
		for _, child := range value {
			asyncQuestionWalk(child, visit)
		}
	}
}

func asyncQuestionButtonPayload(t *testing.T, elements any, label string) map[string]any {
	t.Helper()
	var found map[string]any
	asyncQuestionWalk(elements, func(element map[string]any) {
		if element["tag"] != "button" {
			return
		}
		text, _ := element["text"].(map[string]any)
		if text["content"] != label {
			return
		}
		if label == "提交回答" && element["form_action_type"] != "submit" {
			t.Fatal("answer submission is not a form submit", element)
		}
		for _, behavior := range element["behaviors"].([]any) {
			callback := behavior.(map[string]any)
			if callback["type"] == "callback" {
				found, _ = callback["value"].(map[string]any)
			}
		}
	})
	if found == nil {
		t.Fatal("missing rendered button", label)
	}
	return found
}

func asyncQuestionCallbackForTest(t *testing.T, payload map[string]any, fields map[string]any) (control.Action, bool) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"event": map[string]any{
		"operator": map[string]any{"open_id": "ou-answerer"},
		"context":  map[string]any{"open_chat_id": "chat-existing", "open_message_id": "om-question-card"},
		"action":   map[string]any{"value": payload, "form_value": fields},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var callback larkcallback.CardActionTriggerEvent
	if err := json.Unmarshal(raw, &callback); err != nil {
		t.Fatal(err)
	}
	return ParseCardActionTriggerEvent(RoutingEnv{GatewayID: "bot-existing",
		SurfaceForCardAction: func(lookup CardActionSurfaceLookup) string {
			if lookup.MessageID != "om-question-card" || lookup.OperatorID != "ou-answerer" {
				t.Fatal("lost authenticated callback identity", lookup)
			}
			return "surface-existing"
		}}, &callback)
}

func TestAsyncQuestionRenderedFormRoundTripsBoundedAnswersWithoutPromptText(t *testing.T) {
	elements := asyncQuestionTestElements(t, asyncQuestionTestPage(), "life-current")
	fields := map[string]bool{}
	asyncQuestionWalk(elements, func(element map[string]any) {
		if element["tag"] == "input" || element["tag"] == "select_static" {
			fields[element["name"].(string)] = true
			if element["default_value"] != nil || element["initial_option"] != nil {
				t.Fatal("new question rendered a previous answer", element)
			}
			if element["label"] != nil || element["label_position"] != nil {
				t.Fatal("question input repeated the title in a cramped side label", element)
			}
			if element["width"] != "fill" {
				t.Fatal("question input did not fill the available form width", element)
			}
		}
	})
	if len(fields) != 3 || !fields["answer_0"] || !fields["choice_1"] || !fields["answer_1"] {
		t.Fatal("questions did not produce independent input fields", fields)
	}
	payload := asyncQuestionButtonPayload(t, elements, "提交回答")
	if len(payload) != 4 || payload[frontstagecontract.CardActionPayloadKeyAsyncQuestionID] != "opaque-question-card" {
		t.Fatal("callback should contain identity, revision and lifecycle only", payload)
	}
	payload["thread_id"], payload["instance_id"] = "forged-thread", "forged-instance"
	action, ok := asyncQuestionCallbackForTest(t, payload, map[string]any{"answer_0": "temperature", "choice_1": "1", "answer_1": "later"})
	if !ok || action.Kind != control.ActionAsyncQuestionSubmit || action.AsyncQuestionRevision != 2 || action.AsyncQuestionID != "opaque-question-card" {
		t.Fatal("rendered callback failed to round-trip", action, ok)
	}
	if action.AsyncQuestionAnswers["choice_1"] != "1" || action.AsyncQuestionAnswers["answer_0"] != "temperature" || len(action.AsyncQuestionAnswers) != 3 {
		t.Fatal("answer fields were lost or merged", action)
	}
	if action.Text != "" || len(action.Inputs) != 0 || action.ThreadID != "" || action.InstanceID != "" {
		t.Fatal("answers or forged targets escaped into the prompt route", action)
	}
	if action.GatewayID != "bot-existing" || action.SurfaceSessionID != "surface-existing" || action.ActorUserID != "ou-answerer" || action.ChatID != "chat-existing" || action.MessageID != "om-question-card" || action.Inbound.CardDaemonLifecycleID != "life-current" {
		t.Fatal("callback lost original routing/freshness context", action)
	}
}

func TestAsyncQuestionContinuationAndDeferralDoNotCarryAnswerInput(t *testing.T) {
	page := asyncQuestionTestPage()
	page.AsyncQuestion.CanSubmit, page.AsyncQuestion.CanContinue = false, true
	elements := asyncQuestionTestElements(t, page, "life-current")
	asyncQuestionWalk(elements, func(element map[string]any) {
		if element["tag"] == "form" || element["tag"] == "input" {
			t.Fatal("continue page exposed a fresh answer form")
		}
	})
	for label, want := range map[string]control.ActionKind{"在原会话继续": control.ActionAsyncQuestionContinue, "稍后处理": control.ActionAsyncQuestionDefer} {
		payload := asyncQuestionButtonPayload(t, elements, label)
		action, ok := asyncQuestionCallbackForTest(t, payload, nil)
		if !ok || action.Kind != want || len(action.AsyncQuestionAnswers) != 0 || action.Text != "" {
			t.Fatal("navigation changed answer content", action, ok)
		}
		if _, ok := asyncQuestionCallbackForTest(t, payload, map[string]any{"answer_0": "unexpected"}); ok {
			t.Fatal("non-submit action accepted answer data")
		}
	}
}

func TestAsyncQuestionMalformedCallbacksFailClosed(t *testing.T) {
	base := asyncQuestionButtonPayload(t, asyncQuestionTestElements(t, asyncQuestionTestPage(), "life"), "提交回答")
	for name, fields := range map[string]map[string]any{
		"unknown": {"other": "x"}, "question index": {"answer_8": "x"}, "leading index": {"answer_00": "x"},
		"nonstring": {"answer_0": []any{"x"}}, "large answer": {"answer_0": strings.Repeat("x", 4097)},
		"bad choice": {"choice_0": "-1"}, "choice bound": {"choice_0": "20"}, "choice alias": {"choice_0": "01"},
		"large total": {"answer_0": strings.Repeat("x", 4096), "answer_1": strings.Repeat("x", 4096), "answer_2": strings.Repeat("x", 4096), "answer_3": strings.Repeat("x", 4096), "answer_4": "x"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := asyncQuestionCallbackForTest(t, base, fields); ok {
				t.Fatal("malformed answer payload was accepted")
			}
		})
	}
	for _, revision := range []any{nil, 0, -1, 1.5, "not-a-number", 1_000_000_001} {
		payload := map[string]any{}
		for key, value := range base {
			payload[key] = value
		}
		payload[frontstagecontract.CardActionPayloadKeyAsyncQuestionRevision] = revision
		if _, ok := asyncQuestionCallbackForTest(t, payload, nil); ok {
			t.Fatal("malformed revision was accepted", revision)
		}
	}
}

func TestAsyncQuestionNoninteractiveAndOversizedViewsNeverEmitCallbacks(t *testing.T) {
	for _, which := range []string{"sealed", "read-only", "no lifecycle", "too many", "too large"} {
		t.Run(which, func(t *testing.T) {
			page := asyncQuestionTestPage()
			lifecycle := "life-current"
			switch which {
			case "sealed":
				page.Sealed = true
			case "read-only":
				page.Interactive = false
			case "no lifecycle":
				lifecycle = ""
			case "too many":
				page.AsyncQuestion.Questions = make([]control.AsyncQuestionCardQuestion, 9)
			case "too large":
				page.AsyncQuestion.Questions[0].Title = strings.Repeat("x", 4096)
				page.AsyncQuestion.Questions[1].Title = strings.Repeat("x", 4096)
			}
			asyncQuestionWalk(asyncQuestionTestElements(t, page, lifecycle), func(element map[string]any) {
				if element["tag"] == "form" || element["tag"] == "button" {
					t.Fatal("invalid or noninteractive question view emitted an action", element)
				}
			})
		})
	}
}

func TestAsyncQuestionCallbackRequiresOpaqueIDAndAuthenticatedIdentity(t *testing.T) {
	for _, which := range []string{"missing id", "blank id", "nonstring id", "oversized id", "missing actor", "missing message"} {
		t.Run(which, func(t *testing.T) {
			payload := asyncQuestionButtonPayload(t, asyncQuestionTestElements(t, asyncQuestionTestPage(), "life"), "提交回答")
			actor, message := "ou-answerer", "om-question-card"
			switch which {
			case "missing id":
				delete(payload, frontstagecontract.CardActionPayloadKeyAsyncQuestionID)
			case "blank id":
				payload[frontstagecontract.CardActionPayloadKeyAsyncQuestionID] = " \t "
			case "nonstring id":
				payload[frontstagecontract.CardActionPayloadKeyAsyncQuestionID] = 42
			case "oversized id":
				payload[frontstagecontract.CardActionPayloadKeyAsyncQuestionID] = strings.Repeat("x", 513)
			case "missing actor":
				actor = ""
			case "missing message":
				message = ""
			}
			raw, err := json.Marshal(map[string]any{"event": map[string]any{
				"operator": map[string]any{"open_id": actor},
				"context":  map[string]any{"open_chat_id": "chat-existing", "open_message_id": message},
				"action":   map[string]any{"value": payload, "form_value": map[string]any{"answer_0": "answer"}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			var callback larkcallback.CardActionTriggerEvent
			if err := json.Unmarshal(raw, &callback); err != nil {
				t.Fatal(err)
			}
			if action, ok := ParseCardActionTriggerEvent(RoutingEnv{GatewayID: "bot-existing",
				SurfaceForCardAction: func(CardActionSurfaceLookup) string { return "surface-existing" }}, &callback); ok {
				t.Fatal("callback without trusted identity was admitted", action)
			}
		})
	}
}

func TestAsyncQuestionCallbackPreservesLifecycleForIngressValidation(t *testing.T) {
	for _, lifecycle := range []string{"", "old-daemon", "current-daemon"} {
		payload := asyncQuestionButtonPayload(t, asyncQuestionTestElements(t, asyncQuestionTestPage(), "render-daemon"), "提交回答")
		payload[frontstagecontract.CardActionPayloadKeyDaemonLifecycleID] = lifecycle
		action, ok := asyncQuestionCallbackForTest(t, payload, nil)
		if !ok || action.Inbound == nil || action.Inbound.CardDaemonLifecycleID != lifecycle {
			t.Fatal("gateway changed the lifecycle stamp before the ingress freshness gate", lifecycle, action, ok)
		}
	}
}
