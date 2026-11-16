package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
)

func externalAsyncAnswer(t *testing.T, thread, id string) agentproto.Event {
	t.Helper()
	wire, _ := json.Marshal(map[string]any{
		"kind": agentproto.EventItemCompleted, "threadId": thread, "turnId": "later-turn", "itemId": "reply", "itemKind": "user_message",
		"asyncQuestionAnswers": []any{map[string]any{"questionItemId": id, "answer": "网络"}},
	})
	var event agentproto.Event
	if err := json.Unmarshal(wire, &event); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestAsyncQuestionCrossClientPartialAnswerKeepsOnlyRemainingForm(t *testing.T) {
	s, _, q, _ := asyncCardFixture(t)
	q.Questions = append(q.Questions, agentproto.AsyncQuestion{ID: "second-q", Title: "Next question"})
	stale := asyncCardAction(q, control.ActionAsyncQuestionSubmit)
	s.ApplyAgentEvent("local", externalAsyncAnswer(t, "thread", "native-q"))
	view := s.asyncQuestionCard(q, false).PageView
	if q.Phase != "pending" || view.AsyncQuestion == nil || len(view.AsyncQuestion.Questions) != 1 || view.AsyncQuestion.Questions[0].ID != "second-q" {
		t.Fatal("partial native reply closed or repeated an unanswered question")
	}
	for _, event := range s.ApplySurfaceAction(stale) {
		if event.Command != nil {
			t.Fatal("stale full-form callback submitted answered questions again")
		}
	}
	action := asyncCardAction(q, control.ActionAsyncQuestionSubmit)
	action.AsyncQuestionAnswers = map[string]string{"answer_0": "second answer"}
	asyncCommand(t, s.ApplySurfaceAction(action), agentproto.CommandThreadHistoryRead)
	if len(q.SubmittedAnswers) != 2 || strings.Contains(q.Answer, `"questionItemId":"native-q"`) || !strings.Contains(q.Answer, "second-q") {
		t.Fatal("remaining form lost its receipt or resubmitted the external answer")
	}
}

func TestAsyncQuestionCrossClientScopeAndTerminalProtection(t *testing.T) {
	for _, name := range []string{"other instance", "other thread", "other question", "assistant", "internal helper", "sensitive", "expired", "submitting", "queued"} {
		t.Run(name, func(t *testing.T) {
			s, _, q, _ := asyncCardFixture(t)
			event := externalAsyncAnswer(t, "thread", "native-q")
			instance := "local"
			switch name {
			case "other instance":
				instance = "another"
			case "other thread":
				event.ThreadID = "another"
			case "other question":
				event.AsyncQuestionAnswers[0].QuestionItemID = "another"
			case "assistant":
				event.ItemKind = "agent_message"
			case "internal helper":
				event.TrafficClass = agentproto.TrafficClassInternalHelper
			default:
				q.Phase = name
			}
			phase, revision := q.Phase, q.Revision
			s.resolveExternalAsyncQuestions(instance, event)
			if q.Phase != phase || q.Revision != revision || len(q.SubmittedAnswers) != 0 {
				t.Fatal("unrelated, sensitive or in-flight question was modified")
			}
		})
	}
}

func TestAsyncQuestionCrossClientAnswerBeforeDeliveryPatchesAfterTracking(t *testing.T) {
	s, _, q, now := asyncCardFixture(t)
	q.MessageID = ""
	s.ApplyAgentEvent("local", externalAsyncAnswer(t, "thread", "native-q"))
	if !q.NeedsProjection || q.Phase != "answered" {
		t.Fatal("answer before gateway delivery was lost")
	}
	s.RecordPageTrackingMessage(q.SurfaceID, q.ID, "late-card")
	events := s.tickAsyncQuestions(*now)
	if len(events) != 1 || events[0].PageView == nil || !events[0].PageView.Sealed || events[0].PageView.MessageID != "late-card" || q.NeedsProjection {
		t.Fatal("late-delivered form did not become a sealed receipt")
	}
	if len(s.tickAsyncQuestions(*now)) != 0 {
		t.Fatal("late delivery caused a repeated patch")
	}
}

func TestAsyncQuestionCrossClientAnswerCancelsOnlyUnsentProbe(t *testing.T) {
	s, _, q, _ := asyncCardFixture(t)
	asyncCommand(t, s.ApplySurfaceAction(asyncCardAction(q, control.ActionAsyncQuestionSubmit)), agentproto.CommandThreadHistoryRead)
	commandID := q.CommandID
	s.ApplyAgentEvent("local", externalAsyncAnswer(t, "thread", "native-q"))
	if q.Phase != "answered" || q.CommandID != "" || q.Answer != "" || commandID == "" {
		t.Fatal("native answer did not cancel the unsent local probe")
	}
	if len(s.applyAsyncQuestionProbeResult("local", agentproto.Event{CommandID: commandID})) != 0 {
		t.Fatal("late probe result dispatched a duplicate answer")
	}
}

func TestAsyncQuestionCrossClientAnswerSealsCardWithoutDispatch(t *testing.T) {
	s, surface, q, _ := asyncCardFixture(t)
	active := surface.ActiveQueueItemID
	old := asyncCardAction(q, control.ActionAsyncQuestionSubmit)
	events := s.ApplyAgentEvent("local", externalAsyncAnswer(t, "thread", "native-q"))
	if q.Phase != "answered" || len(events) == 0 {
		t.Fatalf("external answer left the original card editable: phase=%s events=%#v", q.Phase, events)
	}
	if view := s.asyncQuestionCard(q, false).PageView; !view.Sealed || view.AsyncQuestion != nil || len(view.AsyncQuestionReceiptSections) == 0 {
		t.Fatal("answered card must show a read-only receipt")
	}
	if surface.ActiveQueueItemID != active || len(surface.QueuedQueueItemIDs) != 0 {
		t.Fatal("observing an external answer modified the execution queue")
	}
	for _, e := range append(events, s.ApplySurfaceAction(old)...) {
		if e.Command != nil {
			t.Fatal("external answer or stale callback sent another command")
		}
	}
	if duplicate := s.ApplyAgentEvent("local", externalAsyncAnswer(t, "thread", "native-q")); len(duplicate) != 0 {
		t.Fatal("duplicate native notification patched the card again")
	}
}
