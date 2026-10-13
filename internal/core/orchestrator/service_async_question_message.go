package orchestrator

import (
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

func isAsyncQuestionMessage(event agentproto.Event, itemKind string) bool {
	questions, _ := event.Metadata["hasAsyncQuestions"].(bool)
	delivery, _ := event.Metadata["messageDelivery"].(string)
	return itemKind == "agent_message" && delivery == "async" && questions
}

func (s *Service) renderAsyncQuestionMessage(instanceID string, event agentproto.Event, text string) []eventcontract.Event {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	// This is a notification, not a native RPC request or a completed turn.
	// Keep turn ownership and queue policy unchanged; expose the existing
	// single-message steer operation so an answer need not wait behind the turn.
	events := s.flushPendingTurnText(instanceID, event.ThreadID, event.TurnID, false)
	text += "\n\n回答方式：在当前对话发送回答；若本轮仍在等待，对你发出的回答添加 👍，可将这一条回答插入当前轮。"
	questions := s.renderTextItem(instanceID, event.ThreadID, event.TurnID, event.ItemID, text, false)
	for i := range questions {
		if questions[i].Block != nil {
			questions[i].Meta.Semantics.VisibilityClass = eventcontract.VisibilityClassAlwaysVisible
		}
	}
	return append(events, questions...)
}
