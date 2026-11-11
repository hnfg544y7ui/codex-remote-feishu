package orchestrator

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func asyncReceiptView(t *testing.T, events []eventcontract.Event, q *asyncQuestionRecord) *control.FeishuPageView {
	t.Helper()
	for _, event := range events {
		if event.PageView != nil && event.PageView.TrackingKey == q.ID {
			if event.PageView.MessageID != q.MessageID || !event.Meta.Attention.Empty() || event.Meta.MessageDelivery.Mutation != eventcontract.MessageMutationPatchSameMessage {
				t.Fatal("answer receipt moved to another card or repeated the mention")
			}
			return event.PageView
		}
	}
	t.Fatal("answer state update did not include its original card")
	return nil
}

func asyncReceiptText(view *control.FeishuPageView) string {
	var lines []string
	for _, section := range view.BodySections {
		lines = append(lines, section.Label)
		lines = append(lines, section.Lines...)
	}
	for _, section := range view.AsyncQuestionReceiptSections {
		lines = append(lines, section.Label)
		lines = append(lines, section.Lines...)
	}
	return strings.Join(lines, "\n")
}

func TestAsyncQuestionReceiptPreservesActualAnswerAfterAck(t *testing.T) {
	s, _, q, _ := asyncCardFixture(t)
	q.Text = q.Questions[0].Title + "\n- 网络\n- 磁盘"
	action := asyncCardAction(q, control.ActionAsyncQuestionSubmit)
	action.AsyncQuestionAnswers = map[string]string{"choice_0": "1", "answer_0": "只读检查\n标记B"}
	expected := "磁盘\n补充：只读检查\n标记B"
	events := s.ApplySurfaceAction(action)
	view := asyncReceiptView(t, events, q)
	if view.Title != "回答提交中" || !strings.Contains(asyncReceiptText(view), expected) || !view.Sealed || len(q.SubmittedAnswers) != 1 {
		t.Fatal("submitted answer disappeared before backend confirmation")
	}
	action.AsyncQuestionAnswers["answer_0"] = "changed callback map"
	if q.SubmittedAnswers[0].Answer != expected {
		t.Fatal("receipt retained a mutable callback map")
	}
	events = s.ApplyAgentEvent("local", asyncProbe(q, q.TurnID))
	cmd := asyncCommand(t, events, agentproto.CommandTurnSteer)
	wire := strings.TrimSuffix(strings.TrimPrefix(cmd.Prompt.Inputs[0].Text, "<send_user_message_question_reply>\n"), "\n</send_user_message_question_reply>")
	var sent []asyncQuestionAnswer
	if err := json.Unmarshal([]byte(wire), &sent); err != nil || !reflect.DeepEqual(sent, q.SubmittedAnswers) {
		t.Fatal("displayed answer differs from the actual submitted payload", err)
	}
	view = asyncReceiptView(t, s.HandleCommandAccepted("local", agentproto.CommandAck{CommandID: cmd.CommandID, Accepted: true}), q)
	text := asyncReceiptText(view)
	if view.Title != "回答已接收" || !view.Sealed || q.Answer != "" || q.CommandID != "" || !strings.Contains(text, expected) || strings.Count(text, q.Questions[0].Title) != 1 || strings.Contains(text, "send_user_message_question_reply") {
		t.Fatal("ACK cleared the visible receipt, duplicated the question, or retained a sendable payload")
	}
	before := q.SubmittedAnswers[0]
	s.ApplySurfaceAction(asyncCardAction(q, control.ActionAsyncQuestionSubmit))
	if q.SubmittedAnswers[0] != before || q.Answer != "" {
		t.Fatal("duplicate submit replaced a completed answer receipt")
	}
}

func TestAsyncQuestionReceiptSurvivesRecoveryAndQueueStates(t *testing.T) {
	for _, scenario := range []string{"probe-timeout", "submit-timeout", "rejected", "expired", "continue", "queued", "queue-accepted"} {
		t.Run(scenario, func(t *testing.T) {
			s, surface, q, now := asyncCardFixture(t)
			s.ApplySurfaceAction(asyncCardAction(q, control.ActionAsyncQuestionSubmit))
			var events []eventcontract.Event
			wantTitle, wantPhase := "回答状态待确认", "unknown"
			switch scenario {
			case "probe-timeout":
				*now = now.Add(31 * time.Second)
				events = s.Tick(*now)
				wantTitle, wantPhase = "需要你回答", "pending"
			case "expired":
				*now = now.Add(31 * time.Minute)
				events = s.Tick(*now)
				wantTitle, wantPhase = "提问已失效", "expired"
			case "continue", "queued", "queue-accepted":
				events = s.ApplyAgentEvent("local", asyncProbe(q, "new-turn"))
				wantTitle, wantPhase = "等待继续确认", "continue"
				if scenario != "continue" {
					events = s.ApplySurfaceAction(asyncCardAction(q, control.ActionAsyncQuestionContinue))
					wantTitle, wantPhase = "回答已排队", "queued"
					if scenario == "queue-accepted" {
						surface.QueueItems[q.QueueID].Status = state.QueueItemRunning
						events = s.Tick(*now)
						wantTitle, wantPhase = "回答已接收", "answered"
					}
				}
			default:
				s.ApplyAgentEvent("local", asyncProbe(q, q.TurnID))
				if scenario == "rejected" {
					events = s.HandleCommandAccepted("local", agentproto.CommandAck{CommandID: q.CommandID, Accepted: false})
				} else {
					*now = now.Add(31 * time.Second)
					events = s.Tick(*now)
				}
			}
			view := asyncReceiptView(t, events, q)
			if q.Phase != wantPhase || view.Title != wantTitle || !strings.Contains(asyncReceiptText(view), "网络\n补充：只读") {
				t.Fatal("answer receipt or truthful delivery state was lost", q.Phase, view.Title)
			}
			if scenario != "continue" && q.Answer != "" {
				t.Fatal("read-only receipt retained a dispatchable answer")
			}
			if scenario == "probe-timeout" {
				if !strings.Contains(asyncReceiptText(view), "上次提交的回答") || view.AsyncQuestion == nil || !view.AsyncQuestion.CanSubmit {
					t.Fatal("retry form lost its previous-answer receipt")
				}
				invalid := asyncCardAction(q, control.ActionAsyncQuestionSubmit)
				invalid.AsyncQuestionAnswers = map[string]string{}
				s.ApplySurfaceAction(invalid)
				if q.SubmittedAnswers[0].Answer != "网络\n补充：只读" || q.Answer != "" {
					t.Fatal("invalid retry replaced the validated previous answer")
				}
				retry := asyncCardAction(q, control.ActionAsyncQuestionSubmit)
				retry.AsyncQuestionAnswers = map[string]string{"answer_0": "重新填写"}
				view = asyncReceiptView(t, s.ApplySurfaceAction(retry), q)
				if q.SubmittedAnswers[0].Answer != "重新填写" || strings.Contains(asyncReceiptText(view), "补充：只读") {
					t.Fatal("valid retry did not replace the old visible snapshot")
				}
			}
		})
	}
}
