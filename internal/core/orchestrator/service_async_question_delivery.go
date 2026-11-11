package orchestrator

import (
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func (s *Service) asyncQuestionForCommand(commandID string) *asyncQuestionRecord {
	if commandID == "" {
		return nil
	}
	for _, q := range s.turns.asyncQuestions {
		if q.CommandID == commandID {
			return q
		}
	}
	return nil
}

func (s *Service) AsyncQuestionCommandInstanceID(commandID string) string {
	if q := s.asyncQuestionForCommand(commandID); q != nil && s.asyncQuestionContext(q) && s.now().Before(q.Deadline) {
		return q.InstanceID
	}
	return ""
}

func (s *Service) finishAsyncQuestion(q *asyncQuestionRecord, phase, status string) []eventcontract.Event {
	q.Phase, q.Status, q.Answer, q.CommandID = phase, status, "", ""
	q.Revision++
	if q.MessageID == "" {
		return nil
	}
	return []eventcontract.Event{s.asyncQuestionCard(q, false)}
}

func (s *Service) applyAsyncQuestionProbeResult(instanceID string, event agentproto.Event) []eventcontract.Event {
	q := s.asyncQuestionForCommand(event.CommandID)
	if q == nil || q.InstanceID != instanceID || q.Phase != "checking" {
		return nil
	}
	if !s.asyncQuestionContext(q) || !s.now().Before(q.ExpiresAt) {
		return s.finishAsyncQuestion(q, "expired", "原会话不可用，本次未发送回答。")
	}
	if !s.now().Before(q.Deadline) || event.ErrorMessage != "" || event.ThreadID != q.ThreadID || event.ThreadHistory == nil || event.ThreadHistory.Thread.ThreadID != q.ThreadID {
		return s.finishAsyncQuestion(q, "pending", "未能确认原轮次，回答未发送。请重新填写后提交。")
	}
	q.CommandID = ""
	history := event.ThreadHistory
	if history.Thread.RuntimeStatus == nil || history.Thread.RuntimeStatus.Type != agentproto.ThreadRuntimeStatusTypeActive ||
		len(history.Turns) != 1 || history.Turns[0].Status != "inProgress" || history.Turns[0].TurnID != q.TurnID {
		q.Phase, q.Status = "continue", "原轮次已结束或已变化，回答尚未发送。点击“在原会话继续”可将回答作为后续输入排队。"
		q.Revision++
		return []eventcontract.Event{s.asyncQuestionCard(q, false)}
	}
	if s.progress.isCompactTurn(q.InstanceID, q.ThreadID, q.TurnID) {
		return s.finishAsyncQuestion(q, "pending", "原轮次正在整理上下文，回答未发送。请稍后重新填写。")
	}
	q.Phase, q.Status, q.CommandID, q.Deadline = "submitting", "正在发送到原轮次，等待后端确认。", "relay-async-question-answer-"+s.nextAgentCommandID(), s.now().Add(30*time.Second)
	q.Revision++
	surface := s.root.Surfaces[q.SurfaceID]
	return []eventcontract.Event{s.asyncQuestionCard(q, false), {Kind: eventcontract.KindAgentCommand, SurfaceSessionID: q.SurfaceID,
		Command: &agentproto.Command{CommandID: q.CommandID, Kind: agentproto.CommandTurnSteer,
			Origin: agentproto.Origin{Surface: q.SurfaceID, UserID: q.ActorID, ChatID: surface.ChatID, MessageID: q.MessageID},
			Target: agentproto.Target{ThreadID: q.ThreadID, TurnID: q.TurnID}, Prompt: agentproto.Prompt{Inputs: []agentproto.Input{{Type: agentproto.InputText, Text: q.Answer}}}}}}
}

// ACKs for these commands never consume or rebind an ordinary queue item.
func (s *Service) acknowledgeAsyncQuestion(instanceID string, ack agentproto.CommandAck) []eventcontract.Event {
	q := s.asyncQuestionForCommand(ack.CommandID)
	if q == nil || q.InstanceID != instanceID {
		return nil
	}
	if q.Phase == "checking" {
		if !ack.Accepted {
			return s.finishAsyncQuestion(q, "pending", "轮次查询失败，回答未发送。请重新填写后提交。")
		}
		return nil // transport acceptance is not a history result
	}
	if q.Phase != "submitting" {
		return nil
	}
	if !ack.Accepted || !s.now().Before(q.Deadline) {
		return s.finishAsyncQuestion(q, "unknown", "未能确认后端是否接收回答。请核对原会话；不会自动重发。")
	}
	return s.finishAsyncQuestion(q, "answered", "后端已确认接收回答，已送入原执行轮次；任务继续执行。")
}

func (s *Service) failAsyncQuestionDispatch(surfaceID, commandID string) []eventcontract.Event {
	q := s.asyncQuestionForCommand(commandID)
	if q == nil || q.SurfaceID != surfaceID {
		return nil
	}
	if q.Phase == "checking" {
		return s.finishAsyncQuestion(q, "pending", "轮次查询未完成，回答未发送。请重新填写后提交。")
	}
	return s.finishAsyncQuestion(q, "unknown", "回答发送结果无法确认。请核对原会话；不会自动重发。")
}

func (s *Service) continueAsyncQuestion(surface *state.SurfaceConsoleRecord, q *asyncQuestionRecord) []eventcontract.Event {
	if activePendingRequest(surface) != nil || surface.ActiveRequestCapture != nil || surface.ContractRefreshPending || surface.PendingHeadless != nil {
		q.Status = "原会话有其他待处理操作，回答尚未入队。请先处理该操作，再点击继续。"
		return []eventcontract.Event{s.asyncQuestionCard(q, false)}
	}
	if blocked := s.blockFeishuRoomActiveDispatch(surface); blocked != nil {
		return blocked
	}
	inst, thread := s.root.Instances[q.InstanceID], s.root.Instances[q.InstanceID].Threads[q.ThreadID]
	if thread.CWD == "" {
		return s.finishAsyncQuestion(q, "pending", "原会话工作目录不可用，回答未入队。请恢复后重新填写。")
	}
	// Construct an ordinary, frozen continuation without consuming staged files,
	// running natural-language Goal parsing, or changing to a model-group thread.
	item := &state.QueueItemRecord{SurfaceSessionID: q.SurfaceID, ActorUserID: q.ActorID, SourceKind: state.QueueItemSourceUser,
		SourceMessageID: q.MessageID, SourceMessageIDs: []string{q.MessageID}, SourceMessagePreview: "提问卡片的回答", ReplyToMessageID: q.MessageID,
		Inputs: []agentproto.Input{{Type: agentproto.InputText, Text: q.Answer}}, RouteModeAtEnqueue: state.RouteModePinned, Status: state.QueueItemQueued,
		FrozenDispatchPlan: agentproto.DefaultPromptDispatchPlanForExecutionThread(q.ThreadID),
		FrozenOverride:     s.resolveFrozenPromptOverride(inst, surface, q.ThreadID, thread.CWD, surface.PromptOverride), FrozenPlanMode: s.freezePlanModeForPrompt(surface),
		CodexAdmissionRef: s.freezeCodexAdmissionRefForPrompt(surface), CodexConnectionContract: s.freezeCodexConnectionContractForPrompt(surface), CodexThreadPolicy: s.freezeCodexThreadPolicyForPrompt(surface)}
	item.FrozenDispatchPlan.CWD = thread.CWD
	_, goalEvents := s.maybeBeginGoalInterlockForQueueItem(surface, inst, item)
	events := append(goalEvents, s.enqueuePreparedQueueItem(surface, item, false)...)
	if item.ID == "" || surface.QueueItems[item.ID] == nil {
		return events
	}
	q.QueueID, q.Phase, q.Answer, q.Status = item.ID, "queued", "", "回答已加入原会话的正常队列，等待后端接收。"
	q.Revision++
	return append(events, s.asyncQuestionCard(q, false))
}

func (s *Service) tickAsyncQuestions(now time.Time) []eventcontract.Event {
	var events []eventcontract.Event
	for id, q := range s.turns.asyncQuestions {
		terminal := q.Phase == "answered" || q.Phase == "expired" || q.Phase == "unknown" || q.Phase == "sensitive"
		if terminal {
			if now.Sub(q.CreatedAt) > 24*time.Hour {
				delete(s.turns.asyncQuestions, id)
			}
			continue
		}
		if q.Phase == "queued" {
			surface := s.root.Surfaces[q.SurfaceID]
			if surface == nil {
				events = append(events, s.finishAsyncQuestion(q, "unknown", "回答的队列状态不可用，请核对原会话。")...)
				continue
			}
			item := surface.QueueItems[q.QueueID]
			if item == nil || item.Status == state.QueueItemFailed || item.Status == state.QueueItemDiscarded {
				events = append(events, s.finishAsyncQuestion(q, "unknown", "回答未能继续执行，请核对原会话及队列。")...)
			} else if item.Status == state.QueueItemRunning || item.Status == state.QueueItemCompleted {
				events = append(events, s.finishAsyncQuestion(q, "answered", "回答已由后端接收，原会话已开始后续执行。")...)
			}
			continue
		}
		if q.Phase == "submitting" {
			if !s.asyncQuestionContext(q) || !now.Before(q.Deadline) {
				events = append(events, s.finishAsyncQuestion(q, "unknown", "回答接收状态未知，请核对原会话；不会自动重发。")...)
			}
		} else if !s.asyncQuestionContext(q) || !now.Before(q.ExpiresAt) {
			events = append(events, s.finishAsyncQuestion(q, "expired", "原会话不可用或卡片已过期，本次未发送回答。")...)
		} else if q.Phase == "checking" && !now.Before(q.Deadline) {
			events = append(events, s.finishAsyncQuestion(q, "pending", "查询超时，回答未发送。请重新填写后提交。")...)
		}
	}
	return events
}

func isAsyncQuestionCommand(id string) bool { return strings.HasPrefix(id, "relay-async-question-") }
