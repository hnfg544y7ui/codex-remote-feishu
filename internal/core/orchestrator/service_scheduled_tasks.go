package orchestrator

import (
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

// ScheduledPromptRequest captures a durable task's exact destination. It never
// borrows the chat's selected thread or staged attachments at execution time.
type ScheduledPromptRequest struct {
	RunID, GatewayID, SurfaceID, ChatID, ActorUserID string
	Workspace, CWD, ThreadID, Prompt, Mode           string
}

type ScheduledPromptResult struct {
	QueueItemID, InstanceID, SurfaceID, WaitReason string
	Events                                         []eventcontract.Event
}

type ScheduledRunResult struct {
	Found, Terminal                            bool
	QueueItemID, InstanceID, SurfaceID, Status string
	TerminalCause, ResultText, ResultError     string
	ThreadID, TurnID                           string
}

// TryEnqueueScheduledPrompt reuses the existing writer and dispatch arbitration.
// An unavailable route stays in the caller's durable waiting state; this method
// never claims another chat's workspace, launches a competing writer, or changes
// the user's selection to make a background task eligible.
func (s *Service) TryEnqueueScheduledPrompt(req ScheduledPromptRequest) ScheduledPromptResult {
	result := ScheduledPromptResult{SurfaceID: strings.TrimSpace(req.SurfaceID)}
	if s == nil || s.root == nil || strings.TrimSpace(req.RunID) == "" || strings.TrimSpace(req.Prompt) == "" || strings.TrimSpace(req.ActorUserID) == "" {
		result.WaitReason = "invalid_request"
		return result
	}
	if existing := s.ScheduledRunOutcome(req.RunID); existing.Found {
		result.QueueItemID, result.InstanceID, result.SurfaceID = existing.QueueItemID, existing.InstanceID, existing.SurfaceID
		return result
	}
	surface := s.root.Surfaces[result.SurfaceID]
	if surface == nil {
		result.WaitReason = "surface_unavailable"
		return result
	}
	// A group surface's last actor changes as different members speak. The task
	// actor belongs to the immutable request, while gateway/chat identify origin.
	if surface.GatewayID != strings.TrimSpace(req.GatewayID) || surface.ChatID != strings.TrimSpace(req.ChatID) {
		result.WaitReason = "owner_changed"
		return result
	}
	inst := s.root.Instances[surface.AttachedInstanceID]
	if inst == nil || !inst.Online || surface.PendingHeadless != nil || surface.Abandoning {
		result.WaitReason = "instance_unavailable"
		return result
	}
	workspace := normalizeWorkspaceClaimKey(req.Workspace)
	if workspace == "" || workspace != instanceWorkspaceClaimKey(inst) {
		result.WaitReason = "workspace_unavailable"
		return result
	}
	if state.EffectiveInstanceBackend(inst) != agentproto.BackendCodex {
		result.WaitReason = "backend_unsupported"
		return result
	}
	if owner := s.instanceClaimSurface(inst.InstanceID); owner != nil && owner.SurfaceSessionID != surface.SurfaceSessionID {
		result.WaitReason = "instance_owned_elsewhere"
		return result
	}
	// Detached/background remote turns intentionally do not overwrite the
	// instance's active selection. Check their binding as well as ActiveTurnID.
	if binding := s.activeRemoteBinding(inst.InstanceID, ""); binding != nil && binding.SurfaceSessionID != surface.SurfaceSessionID {
		result.WaitReason = "instance_busy_elsewhere"
		return result
	}
	plan := agentproto.PromptDispatchPlan{CWD: strings.TrimSpace(req.Workspace), SurfaceBindingPolicy: agentproto.SurfaceBindingPolicyKeepSurfaceSelection}
	if strings.TrimSpace(req.CWD) != "" {
		plan.CWD = strings.TrimSpace(req.CWD)
	}
	if !cwdBelongsToInstanceWorkspace(inst, plan.CWD) {
		result.WaitReason = "workspace_unavailable"
		return result
	}
	switch strings.TrimSpace(req.Mode) {
	case "existing":
		plan.ExecutionMode = agentproto.PromptExecutionModeResumeExisting
		plan.ExecutionThreadID = strings.TrimSpace(req.ThreadID)
		thread := inst.Threads[plan.ExecutionThreadID]
		if plan.ExecutionThreadID == "" || !threadVisible(thread) || !threadBelongsToInstanceWorkspace(inst, thread) {
			result.WaitReason = "thread_unavailable"
			return result
		}
		if cwd := strings.TrimSpace(thread.CWD); cwd != "" {
			plan.CWD = cwd
		}
		if owner := s.threadClaimSurface(plan.ExecutionThreadID); owner != nil && owner.SurfaceSessionID != surface.SurfaceSessionID {
			result.WaitReason = "thread_owned_elsewhere"
			return result
		}
		for _, other := range s.root.Instances {
			if other.InstanceID == inst.InstanceID || !other.Online {
				continue
			}
			if held := other.Threads[plan.ExecutionThreadID]; held != nil && held.Loaded {
				result.WaitReason = "thread_writer_elsewhere"
				return result
			}
		}
	case "new_each_run":
		plan.ExecutionMode = agentproto.PromptExecutionModeStartNew
	default:
		result.WaitReason = "mode_unsupported"
		return result
	}
	if blocked := s.blockFeishuRoomActiveDispatch(surface); blocked != nil {
		result.WaitReason = "room_busy"
		return result
	}
	if surface.QueueItems == nil {
		surface.QueueItems = map[string]*state.QueueItemRecord{}
	}
	item := &state.QueueItemRecord{
		ScheduledRunID:      strings.TrimSpace(req.RunID),
		ScheduledInstanceID: inst.InstanceID,
		ScheduledWorkspace:  workspace,
		SurfaceSessionID:    surface.SurfaceSessionID, ActorUserID: strings.TrimSpace(req.ActorUserID),
		SourceKind:              state.QueueItemSourceKind("scheduled_task"),
		Inputs:                  []agentproto.Input{{Type: agentproto.InputText, Text: strings.TrimSpace(req.Prompt)}},
		FrozenDispatchPlan:      agentproto.NormalizePromptDispatchPlan(plan),
		FrozenOverride:          s.resolveFrozenPromptOverride(inst, surface, plan.ExecutionThreadID, plan.CWD, surface.PromptOverride),
		FrozenPlanMode:          s.freezePlanModeForPrompt(surface),
		CodexAdmissionRef:       s.freezeCodexAdmissionRefForPrompt(surface),
		CodexConnectionContract: s.freezeCodexConnectionContractForPrompt(surface),
		CodexThreadPolicy:       s.freezeCodexThreadPolicyForPrompt(surface),
		OpenCodeAdmissionRef:    s.freezeOpenCodeAdmissionRefForPrompt(surface),
		RouteModeAtEnqueue:      state.RouteModePinned, Status: state.QueueItemQueued,
	}
	// A run ID is not a Feishu message ID. Leave both source and reply anchors
	// empty; durable result delivery belongs to the scheduler's original chat.
	result.Events = s.enqueuePreparedQueueItem(surface, item, false)
	result.QueueItemID, result.InstanceID = item.ID, inst.InstanceID
	return result
}

// ScheduledRunOutcome exposes terminal evidence captured by dispatch core,
// including interruption and failure, rather than equating queue completion with
// model success. Queue records are process-local; durable recovery is caller-owned.
func (s *Service) ScheduledRunOutcome(runID string) ScheduledRunResult {
	if s == nil || s.root == nil {
		return ScheduledRunResult{}
	}
	for _, surface := range s.root.Surfaces {
		for _, item := range surface.QueueItems {
			if item == nil || item.ScheduledRunID == "" || item.ScheduledRunID != strings.TrimSpace(runID) {
				continue
			}
			cause := item.TerminalCause
			if cause == "" {
				switch item.Status {
				case state.QueueItemDiscarded:
					cause = "cancelled"
				case state.QueueItemFailed, state.QueueItemCompleted:
					cause = "unknown_terminal"
				}
			}
			return ScheduledRunResult{
				Found: true, Terminal: cause != "", QueueItemID: item.ID,
				InstanceID: item.ScheduledInstanceID, SurfaceID: surface.SurfaceSessionID,
				Status: string(item.Status), TerminalCause: cause,
				ResultText: item.ResultText, ResultError: item.ResultError,
				ThreadID: item.ResultThreadID, TurnID: item.ResultTurnID,
			}
		}
	}
	return ScheduledRunResult{}
}

// ScheduledCommandInstance returns the frozen writer for a scheduled queue's
// agent command; a daemon must not infer it from a later surface attachment.
func (s *Service) ScheduledCommandInstance(surfaceID string) string {
	if s == nil || s.root == nil {
		return ""
	}
	if surface := s.Surface(surfaceID); surface != nil {
		if item := surface.QueueItems[surface.ActiveQueueItemID]; item != nil && item.ScheduledRunID != "" {
			return item.ScheduledInstanceID
		}
	}
	return ""
}

func (s *Service) ScheduledDispatchInstanceID(surfaceID string) string {
	return s.ScheduledCommandInstance(surfaceID)
}

// ScheduledCommandInstanceID validates a buffered command immediately before
// dispatch. A completed/cancelled run or a command already bound to the wire is
// not eligible. Callers must drop an empty result, never use another attachment.
func (s *Service) ScheduledCommandInstanceID(commandID string) string {
	const prefix = "relay-schedule-"
	if s == nil || s.root == nil || !strings.HasPrefix(commandID, prefix) {
		return ""
	}
	runID := strings.TrimPrefix(commandID, prefix)
	if runID == "" {
		return ""
	}
	for _, surface := range s.root.Surfaces {
		item := surface.QueueItems[surface.ActiveQueueItemID]
		if item == nil || item.ScheduledRunID != runID || item.Status != state.QueueItemDispatching || item.TerminalCause != "" {
			continue
		}
		binding := s.pendingRemoteBinding(item.ScheduledInstanceID, "")
		if binding == nil || binding.QueueItemID != item.ID || binding.SurfaceSessionID != surface.SurfaceSessionID {
			return ""
		}
		if binding.CommandID != "" {
			return ""
		}
		inst := s.root.Instances[item.ScheduledInstanceID]
		reason := s.scheduledQueueDispatchWaitReason(surface, inst, item)
		if reason == "" && (inst == nil || !inst.Online) {
			reason = "instance_unavailable"
		}
		if reason == "" && (inst.ActiveTurnID != "" || s.activeRemoteBinding(inst.InstanceID, "") != nil) {
			reason = "instance_busy_before_send"
		}
		if reason == "" && s.progress.instanceHasCompact(inst.InstanceID) {
			reason = "instance_compacting_before_send"
		}
		if reason != "" {
			// No wire command exists. Release only this run's pending ownership;
			// another surface's active turn must survive stale-event rejection.
			s.clearPendingRemoteTurn(item.ScheduledInstanceID)
			s.clearSurfaceActiveQueueItem(surface, item.ID)
			if reason == "scheduled_instance_changed" || reason == "scheduled_workspace_changed" {
				item.Status, item.TerminalCause, item.ResultError = state.QueueItemFailed, "dispatch_failed", reason
				item.ResultThreadID = queuedItemExecutionThreadID(item)
			} else {
				// Temporary contention/offline facts appeared during preceding UI
				// delivery. Restore the unsent item for the normal queue to retry.
				item.Status = state.QueueItemQueued
				surface.QueuedQueueItemIDs = append([]string{item.ID}, surface.QueuedQueueItemIDs...)
			}
			return ""
		}
		return item.ScheduledInstanceID
	}
	return ""
}

// AbortScheduledRunBeforeDispatch rolls back an enqueue whose durable run
// acceptance failed. It never cancels a command that already acquired a wire ID
// or a runtime turn, and deliberately does not dispatch the next queue item.
func (s *Service) AbortScheduledRunBeforeDispatch(runID string) bool {
	if s == nil || s.root == nil {
		return false
	}
	for _, surface := range s.root.Surfaces {
		for _, item := range surface.QueueItems {
			if item == nil || item.ScheduledRunID == "" || item.ScheduledRunID != strings.TrimSpace(runID) {
				continue
			}
			switch item.Status {
			case state.QueueItemQueued:
				for i, id := range surface.QueuedQueueItemIDs {
					if id == item.ID {
						surface.QueuedQueueItemIDs = append(surface.QueuedQueueItemIDs[:i], surface.QueuedQueueItemIDs[i+1:]...)
						break
					}
				}
			case state.QueueItemDispatching:
				binding := s.pendingRemoteBinding(item.ScheduledInstanceID, "")
				if surface.ActiveQueueItemID != item.ID || binding == nil || binding.QueueItemID != item.ID || binding.CommandID != "" || s.activeRemoteBinding(item.ScheduledInstanceID, "") != nil {
					return false
				}
				s.clearPendingRemoteTurn(item.ScheduledInstanceID)
				s.clearSurfaceActiveQueueItem(surface, item.ID)
			default:
				return false
			}
			item.Status, item.TerminalCause = state.QueueItemFailed, "dispatch_failed"
			item.ResultError = "任务运行状态未持久化，派发已取消。"
			item.ResultThreadID = queuedItemExecutionThreadID(item)
			return true
		}
	}
	return false
}

func (s *Service) scheduledQueueDispatchWaitReason(surface *state.SurfaceConsoleRecord, inst *state.InstanceRecord, item *state.QueueItemRecord) string {
	if item.ScheduledRunID == "" {
		return ""
	}
	if item.ScheduledInstanceID == "" || surface.AttachedInstanceID != item.ScheduledInstanceID || inst == nil || inst.InstanceID != item.ScheduledInstanceID {
		return "scheduled_instance_changed"
	}
	plan := queuedItemPromptDispatchPlan(item)
	if item.ScheduledWorkspace == "" || item.ScheduledWorkspace != instanceWorkspaceClaimKey(inst) || !cwdBelongsToInstanceWorkspace(inst, plan.CWD) || state.EffectiveInstanceBackend(inst) != agentproto.BackendCodex {
		return "scheduled_workspace_changed"
	}
	if owner := s.instanceClaimSurface(inst.InstanceID); owner != nil && owner.SurfaceSessionID != surface.SurfaceSessionID {
		return "instance_owned_elsewhere"
	}
	if binding := s.activeRemoteBinding(inst.InstanceID, ""); binding != nil && binding.SurfaceSessionID != surface.SurfaceSessionID {
		return "instance_busy_elsewhere"
	}
	if plan.ExecutionMode == agentproto.PromptExecutionModeStartNew {
		return ""
	}
	thread := inst.Threads[plan.ExecutionThreadID]
	if !threadVisible(thread) || !threadBelongsToInstanceWorkspace(inst, thread) {
		return "thread_unavailable"
	}
	if owner := s.threadClaimSurface(plan.ExecutionThreadID); owner != nil && owner.SurfaceSessionID != surface.SurfaceSessionID {
		return "thread_owned_elsewhere"
	}
	for _, other := range s.root.Instances {
		if other.InstanceID == inst.InstanceID || !other.Online {
			continue
		}
		if held := other.Threads[plan.ExecutionThreadID]; held != nil && held.Loaded {
			return "thread_writer_elsewhere"
		}
	}
	return ""
}
