package orchestrator

import (
	"fmt"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

type steerTargetProbe struct {
	SurfaceID, ActorID, InstanceID, ThreadID, MessageID string
	QueueIDs                                            []string
	ExpiresAt                                           time.Time
}

func (s *Service) SteerTargetCommandInstanceID(commandID string) string {
	if probe, ok := s.turns.steerTargetProbes[commandID]; ok {
		return probe.InstanceID
	}
	if _, binding := s.pendingSteerForCommand("", commandID); binding != nil {
		return binding.InstanceID
	}
	return ""
}

func (s *Service) steerTargetProbeFeedback(binding steerTargetProbe, text string, pending bool) []eventcontract.Event {
	surface := s.root.Surfaces[binding.SurfaceID]
	if surface == nil {
		return nil
	}
	if binding.MessageID != "" {
		title, theme := "无法确认当前执行轮次", "system"
		if pending {
			title, theme = "正在确认当前执行轮次", "progress"
		}
		return []eventcontract.Event{steerAllOwnerCardEvent(binding.SurfaceID, binding.MessageID, title, theme, !pending, text)}
	}
	code := "steer_target_unavailable"
	if pending {
		code = "steer_target_pending"
	}
	return notice(surface, code, text)
}

func (s *Service) beginSteerTargetProbe(surface *state.SurfaceConsoleRecord, messageID string) ([]eventcontract.Event, bool) {
	if surface == nil {
		return nil, false
	}
	threadID := strings.TrimSpace(surface.SelectedThreadID)
	candidates := s.steerCandidates(surface, threadID)
	if len(candidates) == 0 {
		return nil, false
	}
	binding := steerTargetProbe{SurfaceID: surface.SurfaceSessionID, ActorID: surface.ActorUserID,
		InstanceID: surface.AttachedInstanceID, ThreadID: threadID, MessageID: messageID, ExpiresAt: s.now().Add(30 * time.Second)}
	for _, candidate := range candidates {
		binding.QueueIDs = append(binding.QueueIDs, candidate.Item.ID)
	}
	inst := s.root.Instances[binding.InstanceID]
	if inst == nil || !inst.Online || s.surfaceBackend(surface) != agentproto.BackendCodex ||
		!ordinaryThreadVisible(inst.Threads[threadID]) || !s.surfaceOwnsThread(surface, threadID) {
		return s.steerTargetProbeFeedback(binding, "有排队消息，但当前会话不可用，暂时无法并入执行。消息已保留。", false), true
	}
	thread := inst.Threads[threadID]
	if inst.ActiveTurnID != "" || thread.RuntimeStatus == nil || thread.RuntimeStatus.Type != "active" {
		return s.steerTargetProbeFeedback(binding, fmt.Sprintf("当前没有可确认的执行轮次，%d 条消息继续排队；尚未并入或重新发送。", len(candidates)), false), true
	}
	for _, pending := range s.turns.steerTargetProbes {
		if pending.SurfaceID == binding.SurfaceID {
			text := "已有查询正在进行，排队消息尚未发送，请等待原请求的结果。"
			if messageID != "" {
				return []eventcontract.Event{steerAllOwnerCardEvent(binding.SurfaceID, messageID, "已有查询正在进行", "system", true, text)}, true
			}
			return notice(surface, "steer_target_pending", text), true
		}
	}
	commandID := "relay-steer-target-" + s.nextAgentCommandID()
	s.turns.steerTargetProbes[commandID] = binding
	events := s.steerTargetProbeFeedback(binding, "已有排队消息，正在只读查询当前执行轮次。", true)
	events = append(events, eventcontract.Event{Kind: eventcontract.KindAgentCommand, SurfaceSessionID: binding.SurfaceID,
		Command: &agentproto.Command{CommandID: commandID, Kind: agentproto.CommandThreadHistoryRead,
			Target: agentproto.Target{ThreadID: threadID}, HistoryRead: &agentproto.ThreadHistoryReadOptions{LatestTurnOnly: true}}})
	return events, true
}

func (s *Service) failSteerTargetProbe(commandID, text string) []eventcontract.Event {
	binding, ok := s.turns.steerTargetProbes[commandID]
	if !ok {
		return nil
	}
	delete(s.turns.steerTargetProbes, commandID)
	return s.steerTargetProbeFeedback(binding, text, false)
}

func (s *Service) applySteerTargetProbeResult(instanceID string, event agentproto.Event) []eventcontract.Event {
	binding, ok := s.turns.steerTargetProbes[event.CommandID]
	if !ok || instanceID != binding.InstanceID {
		return nil
	}
	if !s.now().Before(binding.ExpiresAt) {
		return s.failSteerTargetProbe(event.CommandID, "当前执行轮次查询超时，排队消息未变。请重新发送 /steerall。")
	}
	surface := s.root.Surfaces[binding.SurfaceID]
	inst := s.root.Instances[binding.InstanceID]
	if surface == nil || inst == nil || !inst.Online || surface.AttachedInstanceID != binding.InstanceID ||
		surface.SelectedThreadID != binding.ThreadID || surface.ActorUserID != binding.ActorID ||
		s.surfaceBackend(surface) != agentproto.BackendCodex || !ordinaryThreadVisible(inst.Threads[binding.ThreadID]) || !s.surfaceOwnsThread(surface, binding.ThreadID) {
		return s.failSteerTargetProbe(event.CommandID, "会话上下文已变化，本次并入已取消，排队消息未变。")
	}
	if event.ErrorMessage != "" || event.ThreadID != binding.ThreadID || event.ThreadHistory == nil || event.ThreadHistory.Thread.ThreadID != binding.ThreadID {
		return s.failSteerTargetProbe(event.CommandID, "无法确认原会话的当前执行轮次，排队消息未变。请稍后重试 /steerall。")
	}
	turns := event.ThreadHistory.Turns
	status := event.ThreadHistory.Thread.RuntimeStatus
	if status == nil || status.Type != agentproto.ThreadRuntimeStatusTypeActive || len(turns) != 1 || strings.TrimSpace(turns[0].TurnID) == "" || turns[0].Status != "inProgress" {
		return s.failSteerTargetProbe(event.CommandID, "当前轮次已结束或尚未开始，消息继续排队；本次没有并入执行。")
	}
	if s.progress.isCompactTurn(binding.InstanceID, binding.ThreadID, turns[0].TurnID) {
		return s.failSteerTargetProbe(event.CommandID, "当前正在整理上下文，消息继续排队。整理完成后可重试 /steerall。")
	}
	delete(s.turns.steerTargetProbes, event.CommandID)
	selected := make(map[string]bool, len(binding.QueueIDs))
	for _, id := range binding.QueueIDs {
		selected[id] = true
	}
	var candidates []steerCandidate
	for _, candidate := range s.steerCandidates(surface, binding.ThreadID) {
		if selected[candidate.Item.ID] {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		return s.steerTargetProbeFeedback(binding, "本次选中的排队消息已撤回或处理，无需再并入执行。", false)
	}
	var events []eventcontract.Event
	if binding.MessageID != "" {
		events = append(events, steerAllRequestedOwnerCardEvent(binding.SurfaceID, binding.MessageID, len(candidates)))
	} else {
		events = notice(surface, "steer_all_requested", fmt.Sprintf("正在把 %d 条排队输入并入当前执行。", len(candidates)))
	}
	dispatch := s.dispatchSteerCandidates(surface, inst, binding.ThreadID, turns[0].TurnID, candidates, "", binding.MessageID)
	for _, event := range dispatch {
		if event.Command != nil && event.Command.Kind == agentproto.CommandTurnSteer {
			event.Command.CommandID = "relay-steer-resolved-" + s.nextAgentCommandID()
			s.bindPendingSteerCommand(binding.SurfaceID, event.Command.CommandID)
		}
	}
	return append(events, dispatch...)
}

func (s *Service) tickSteerTargetProbes(now time.Time) []eventcontract.Event {
	var events []eventcontract.Event
	for commandID, binding := range s.turns.steerTargetProbes {
		inst, surface := s.root.Instances[binding.InstanceID], s.root.Surfaces[binding.SurfaceID]
		if inst == nil || !inst.Online || surface == nil || !now.Before(binding.ExpiresAt) {
			events = append(events, s.failSteerTargetProbe(commandID, "当前执行轮次查询未完成，排队消息未变。请稍后重试 /steerall。")...)
		}
	}
	return events
}
