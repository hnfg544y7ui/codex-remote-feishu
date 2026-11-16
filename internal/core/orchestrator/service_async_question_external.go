package orchestrator

import (
	"strings"
	"unicode/utf8"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

func remainingAsyncQuestions(q *asyncQuestionRecord) []agentproto.AsyncQuestion {
	var remaining []agentproto.AsyncQuestion
	for _, question := range q.Questions {
		if _, answered := q.ExternalAnswers[question.ID]; !answered {
			remaining = append(remaining, question)
		}
	}
	return remaining
}

func externalAsyncQuestionReceipts(q *asyncQuestionRecord) []asyncQuestionAnswer {
	var receipts []asyncQuestionAnswer
	for _, question := range q.Questions {
		if answer, ok := q.ExternalAnswers[question.ID]; ok {
			receipts = append(receipts, asyncQuestionAnswer{question.ID, question.Title, answer})
		}
	}
	return receipts
}

func (s *Service) resolveExternalAsyncQuestions(instanceID string, event agentproto.Event) []eventcontract.Event {
	if event.ItemKind != "user_message" || event.ThreadID == "" || len(event.AsyncQuestionAnswers) == 0 || len(event.AsyncQuestionAnswers) > 8 ||
		event.TrafficClass == agentproto.TrafficClassInternalHelper {
		return nil
	}
	seen, total := map[string]bool{}, 0
	for _, answer := range event.AsyncQuestionAnswers {
		total += len(answer.Answer)
		if !validExternalAsyncText(answer.QuestionItemID, 512) || seen[answer.QuestionItemID] || !validExternalAsyncText(answer.Answer, 5120) || total > 16384 {
			return nil
		}
		seen[answer.QuestionItemID] = true
	}
	var events []eventcontract.Event
	for _, q := range s.turns.asyncQuestions {
		if q.InstanceID != instanceID || q.ThreadID != event.ThreadID ||
			(q.Phase != "pending" && q.Phase != "deferred" && q.Phase != "checking" && q.Phase != "continue") {
			continue
		}
		changed := false
		for _, question := range q.Questions {
			if _, done := q.ExternalAnswers[question.ID]; done {
				continue
			}
			for _, answer := range event.AsyncQuestionAnswers {
				if answer.QuestionItemID == question.ID {
					if q.ExternalAnswers == nil {
						q.ExternalAnswers = map[string]string{}
					}
					q.ExternalAnswers[question.ID], changed = answer.Answer, true
					break
				}
			}
		}
		if !changed {
			continue
		}
		// A native user-message is authoritative. Cancel an unsent local probe;
		// do not steer, queue or manufacture an ACK for another client's reply.
		q.Answer, q.CommandID = "", ""
		q.SubmittedAnswers = externalAsyncQuestionReceipts(q)
		q.Phase, q.Status = "pending", "部分问题已在其他端回答，请补充剩余问题。"
		if len(remainingAsyncQuestions(q)) == 0 {
			q.Phase, q.Status = "answered", "已在其他端回答，无需重复提交。"
		}
		q.Revision++
		if q.MessageID == "" {
			q.NeedsProjection = true
		} else {
			events = append(events, s.asyncQuestionCard(q, false))
		}
	}
	return events
}

func validExternalAsyncText(value string, max int) bool {
	return len(value) <= max && strings.TrimSpace(value) != "" && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}
