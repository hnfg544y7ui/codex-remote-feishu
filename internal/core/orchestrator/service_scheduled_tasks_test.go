package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func scheduledTestSetup(t *testing.T) (*Service, ScheduledPromptRequest) {
	t.Helper()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	s := newServiceForTest(&now)
	inst := threadLifecycleInstance()
	inst.Backend, inst.Source = agentproto.BackendCodex, "headless"
	inst.Threads["thread-2"] = &state.ThreadRecord{ThreadID: "thread-2", CWD: inst.WorkspaceRoot, Loaded: true}
	s.UpsertInstance(inst)
	s.MaterializeSurface("surface-1", "app-1", "chat-1", "user-1")
	surface := s.Surface("surface-1")
	surface.AttachedInstanceID, surface.SelectedThreadID = inst.InstanceID, "thread-2"
	surface.Backend, surface.RouteMode = agentproto.BackendCodex, state.RouteModePinned
	return s, ScheduledPromptRequest{RunID: "run-1", GatewayID: "app-1", SurfaceID: "surface-1", ChatID: "chat-1", ActorUserID: "user-1", Workspace: inst.WorkspaceRoot, ThreadID: "thread-1", Prompt: "只读检查运行状态", Mode: "existing"}
}

func TestScheduledRunOutcomeRecognizesDiscardWithoutAssumingSuccess(t *testing.T) {
	s, req := scheduledTestSetup(t)
	s.Instance("inst-1").ActiveTurnID = "local-turn"
	result := s.TryEnqueueScheduledPrompt(req)
	item := s.Surface(req.SurfaceID).QueueItems[result.QueueItemID]
	if item == nil {
		t.Fatal("no queue item")
	}
	item.Status = state.QueueItemDiscarded
	outcome := s.ScheduledRunOutcome(req.RunID)
	if !outcome.Terminal || outcome.TerminalCause != "cancelled" {
		t.Fatalf("discard left a run active forever: %+v", outcome)
	}
	item.Status = state.QueueItemCompleted
	outcome = s.ScheduledRunOutcome(req.RunID)
	if !outcome.Terminal || outcome.TerminalCause != "unknown_terminal" {
		t.Fatalf("queue completion assumed model success: %+v", outcome)
	}
}

func TestScheduledPromptUsesFrozenThreadAndLeavesStagedInputs(t *testing.T) {
	s, req := scheduledTestSetup(t)
	surface := s.Surface(req.SurfaceID)
	surface.StagedImages["image-1"] = &state.StagedImageRecord{ImageID: "image-1", SourceMessageID: "om-image", LocalPath: "/tmp/image.png"}
	surface.StagedFiles["file-1"] = &state.StagedFileRecord{FileID: "file-1", SourceMessageID: "om-file", LocalPath: "/tmp/file.txt"}
	result := s.TryEnqueueScheduledPrompt(req)
	command := findAgentCommand(result.Events, agentproto.CommandPromptSend)
	if result.WaitReason != "" || result.QueueItemID == "" || command == nil {
		t.Fatalf("expected dispatch: %#v", result)
	}
	if command.Target.ThreadID != req.ThreadID || command.Target.CWD != req.Workspace || command.Target.SurfaceBindingPolicy != agentproto.SurfaceBindingPolicyKeepSurfaceSelection {
		t.Fatalf("incorrect immutable target: %#v", command.Target)
	}
	if surface.SelectedThreadID != "thread-2" || len(surface.StagedImages) != 1 || len(surface.StagedFiles) != 1 || len(command.Prompt.Inputs) != 1 {
		t.Fatal("background execution changed selection or consumed staged attachments")
	}
	item := surface.QueueItems[result.QueueItemID]
	if item.SourceMessageID != "" || item.ReplyToMessageID != "" || command.Origin.MessageID != "" || item.ActorUserID != req.ActorUserID || item.SourceKind != state.QueueItemSourceKind("scheduled_task") {
		t.Fatalf("scheduled task must have captured actor and no fabricated reply anchor: %#v", item)
	}
	for _, event := range result.Events {
		if event.SourceMessageID != "" {
			t.Fatalf("unexpected Feishu source anchor: %#v", event)
		}
	}
	if again := s.TryEnqueueScheduledPrompt(req); again.QueueItemID != result.QueueItemID || len(again.Events) != 0 || len(surface.QueueItems) != 1 {
		t.Fatalf("same run was enqueued twice: %#v", again)
	}
}

func TestScheduledPromptBusyInstanceQueuesExactTarget(t *testing.T) {
	s, req := scheduledTestSetup(t)
	inst := s.Instance("inst-1")
	inst.ActiveTurnID, inst.ActiveThreadID = "local-turn", "thread-2"
	result := s.TryEnqueueScheduledPrompt(req)
	if result.QueueItemID == "" || result.WaitReason != "" || findAgentCommand(result.Events, agentproto.CommandPromptSend) != nil {
		t.Fatalf("busy writer must queue safely: %#v", result)
	}
	surface := s.Surface(req.SurfaceID)
	surface.SelectedThreadID = "thread-2"
	inst.ActiveTurnID, inst.ActiveThreadID = "", ""
	command := findAgentCommand(s.dispatchNext(surface), agentproto.CommandPromptSend)
	if command == nil || command.Target.ThreadID != "thread-1" || surface.SelectedThreadID != "thread-2" {
		t.Fatalf("queue followed a later selection: %#v", command)
	}
}

func TestScheduledPromptWaitsForOtherSurfaceClaim(t *testing.T) {
	s, req := scheduledTestSetup(t)
	s.MaterializeSurface("other", "app-1", "chat-2", "user-1")
	owner := s.Surface("other")
	owner.AttachedInstanceID, owner.SelectedThreadID = "inst-1", "thread-1"
	s.threadClaims["thread-1"] = &threadClaimRecord{SurfaceSessionID: "other", InstanceID: "inst-1"}
	result := s.TryEnqueueScheduledPrompt(req)
	if result.WaitReason != "thread_owned_elsewhere" || result.QueueItemID != "" || len(s.Surface(req.SurfaceID).QueueItems) != 0 {
		t.Fatalf("must not steal another chat's writer: %#v", result)
	}
}

func TestScheduledPromptWaitsForOtherInstanceWriter(t *testing.T) {
	s, req := scheduledTestSetup(t)
	other := threadLifecycleInstance()
	other.InstanceID = "external-instance"
	s.UpsertInstance(other)
	if result := s.TryEnqueueScheduledPrompt(req); result.WaitReason != "thread_writer_elsewhere" || result.QueueItemID != "" {
		t.Fatalf("must not resume into a competing writer: %#v", result)
	}
}

func TestScheduledPromptWaitsForDetachedOtherChatTurn(t *testing.T) {
	s, req := scheduledTestSetup(t)
	s.bindActiveRemoteTurn("inst-1", &remoteTurnBinding{InstanceID: "inst-1", SurfaceSessionID: "other-chat", ThreadID: "thread-background", TurnID: "turn-background"})
	if result := s.TryEnqueueScheduledPrompt(req); result.WaitReason != "instance_busy_elsewhere" || result.QueueItemID != "" {
		t.Fatalf("background binding is a busy writer even without ActiveTurnID: %#v", result)
	}
}

func TestScheduledPromptNewEachRunCreatesDurableThreadWithoutSelectingIt(t *testing.T) {
	s, req := scheduledTestSetup(t)
	req.Mode = "new_each_run"
	result := s.TryEnqueueScheduledPrompt(req)
	command := findAgentCommand(result.Events, agentproto.CommandPromptSend)
	if command == nil || command.Target.ExecutionMode != agentproto.PromptExecutionModeStartNew || !command.Target.CreateThreadIfMissing || command.Target.InternalHelper || command.Target.ThreadID != "" {
		t.Fatalf("expected a persistent new thread: %#v", command)
	}
	s.ApplyAgentEvent("inst-1", agentproto.Event{Kind: agentproto.EventTurnStarted, ThreadID: "thread-new", TurnID: "turn-new", CWD: req.Workspace,
		Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: req.SurfaceID}})
	if surface := s.Surface(req.SurfaceID); surface.SelectedThreadID != "thread-2" || surface.RouteMode != state.RouteModePinned {
		t.Fatalf("new scheduled thread changed selection: %#v", surface)
	}
	if item := s.Surface(req.SurfaceID).QueueItems[result.QueueItemID]; item.Status != state.QueueItemRunning || queuedItemExecutionThreadID(item) != "thread-new" {
		t.Fatalf("runtime thread was not correlated to the scheduled queue item: %#v", item)
	}
}

func TestScheduledPromptUnavailableRouteAndChangedOwnerWait(t *testing.T) {
	for _, scenario := range []struct {
		name, want string
		change     func(*Service, *ScheduledPromptRequest)
	}{
		{"offline", "instance_unavailable", func(s *Service, _ *ScheduledPromptRequest) { s.Instance("inst-1").Online = false }},
		{"workspace", "workspace_unavailable", func(_ *Service, r *ScheduledPromptRequest) { r.Workspace = "/different" }},
		{"gateway", "owner_changed", func(_ *Service, r *ScheduledPromptRequest) { r.GatewayID = "different" }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			s, req := scheduledTestSetup(t)
			scenario.change(s, &req)
			result := s.TryEnqueueScheduledPrompt(req)
			if result.WaitReason != scenario.want || result.QueueItemID != "" || len(s.Surface(req.SurfaceID).QueueItems) != 0 {
				t.Fatalf("unexpected wait result: %#v", result)
			}
		})
	}
}

func TestScheduledRunOutcomeDoesNotTreatInterruptedAsSuccess(t *testing.T) {
	s, req := scheduledTestSetup(t)
	result := s.TryEnqueueScheduledPrompt(req)
	item := s.Surface(req.SurfaceID).QueueItems[result.QueueItemID]
	item.Status, item.TerminalCause = state.QueueItemCompleted, "user_interrupted"
	item.ResultThreadID, item.ResultTurnID = "thread-1", "turn-1"
	s.Surface(req.SurfaceID).AttachedInstanceID = "later-instance"
	outcome := s.ScheduledRunOutcome(req.RunID)
	if !outcome.Found || !outcome.Terminal || outcome.TerminalCause != "user_interrupted" || outcome.InstanceID != "inst-1" || outcome.TurnID != "turn-1" {
		t.Fatalf("lost correlated terminal evidence: %#v", outcome)
	}
}

func TestScheduledPromptCapturesOriginalActorAfterOtherGroupMemberSpeaks(t *testing.T) {
	s, req := scheduledTestSetup(t)
	s.Surface(req.SurfaceID).ActorUserID = "other-member"
	result := s.TryEnqueueScheduledPrompt(req)
	command := findAgentCommand(result.Events, agentproto.CommandPromptSend)
	if command == nil || command.Origin.UserID != req.ActorUserID {
		t.Fatalf("scheduled actor followed last group speaker: %#v", result)
	}
}

func TestScheduledQueuedDispatchRejectsChangedInstanceAndWorkspace(t *testing.T) {
	for _, mutation := range []string{"instance", "workspace"} {
		t.Run(mutation, func(t *testing.T) {
			s, req := scheduledTestSetup(t)
			inst := s.Instance("inst-1")
			inst.ActiveTurnID = "local-turn"
			result := s.TryEnqueueScheduledPrompt(req)
			if result.QueueItemID == "" {
				t.Fatalf("missing queued run: %#v", result)
			}
			surface := s.Surface(req.SurfaceID)
			inst.ActiveTurnID = ""
			if mutation == "instance" {
				other := threadLifecycleInstance()
				other.InstanceID = "other-instance"
				s.UpsertInstance(other)
				surface.AttachedInstanceID = other.InstanceID
			} else {
				inst.WorkspaceRoot, inst.WorkspaceKey = "/different", "/different"
			}
			if command := findAgentCommand(s.dispatchNext(surface), agentproto.CommandPromptSend); command != nil {
				t.Fatalf("scheduled task followed mutated destination: %#v", command)
			}
			outcome := s.ScheduledRunOutcome(req.RunID)
			if !outcome.Terminal || outcome.TerminalCause != "dispatch_failed" || outcome.InstanceID != "inst-1" {
				t.Fatalf("route mutation lacked terminal evidence: %#v", outcome)
			}
		})
	}
}

func TestScheduledQueuedDispatchRechecksOtherWriterAndClaim(t *testing.T) {
	for _, mutation := range []string{"writer", "claim"} {
		t.Run(mutation, func(t *testing.T) {
			s, req := scheduledTestSetup(t)
			inst := s.Instance("inst-1")
			inst.ActiveTurnID = "local-turn"
			result := s.TryEnqueueScheduledPrompt(req)
			inst.ActiveTurnID = ""
			if mutation == "writer" {
				other := threadLifecycleInstance()
				other.InstanceID = "other-writer"
				s.UpsertInstance(other)
			} else {
				s.MaterializeSurface("other-owner", "app-1", "chat-2", "user-1")
				owner := s.Surface("other-owner")
				owner.AttachedInstanceID, owner.SelectedThreadID = "inst-1", "thread-1"
				s.threadClaims["thread-1"] = &threadClaimRecord{SurfaceSessionID: owner.SurfaceSessionID, InstanceID: "inst-1"}
			}
			if command := findAgentCommand(s.dispatchNext(s.Surface(req.SurfaceID)), agentproto.CommandPromptSend); command != nil {
				t.Fatalf("late competing writer was ignored: %#v", command)
			}
			if item := s.Surface(req.SurfaceID).QueueItems[result.QueueItemID]; item.Status != state.QueueItemQueued || item.TerminalCause != "" {
				t.Fatalf("temporary writer contention must keep run queued: %#v", item)
			}
		})
	}
}

func TestScheduledRealLifecycleCapturesTerminalAndDisablesSurfaceFollowups(t *testing.T) {
	for _, status := range []string{"completed", "interrupted", "failed", "eligible_failure"} {
		t.Run(status, func(t *testing.T) {
			s, req := scheduledTestSetup(t)
			surface := s.Surface(req.SurfaceID)
			surface.AutoWhip.Enabled = true
			surface.AutoContinue.Enabled = true
			result := s.TryEnqueueScheduledPrompt(req)
			initiator := agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: req.SurfaceID}
			s.ApplyAgentEvent("inst-1", agentproto.Event{Kind: agentproto.EventTurnStarted, ThreadID: req.ThreadID, TurnID: "scheduled-turn", CWD: req.Workspace, Initiator: initiator})
			s.ApplyAgentEvent("inst-1", agentproto.Event{Kind: agentproto.EventItemCompleted, ThreadID: req.ThreadID, TurnID: "scheduled-turn", ItemID: "answer", ItemKind: "agent_message", Initiator: initiator, Metadata: map[string]any{"text": "我会继续检查。"}})
			if status == "interrupted" {
				s.markRemoteTurnInterruptRequested("inst-1", req.ThreadID, "scheduled-turn")
			}
			errText := ""
			if status == "failed" {
				errText = "failure detail"
			}
			eventStatus := status
			var problem *agentproto.ErrorInfo
			if status == "eligible_failure" {
				eventStatus = "failed"
				problem = &agentproto.ErrorInfo{Layer: "codex", Code: "responseStreamDisconnected", Message: "stream disconnected before completion"}
			}
			s.ApplyAgentEvent("inst-1", agentproto.Event{Kind: agentproto.EventTurnCompleted, ThreadID: req.ThreadID, TurnID: "scheduled-turn", Status: eventStatus, ErrorMessage: errText, Problem: problem, Initiator: initiator})
			outcome := s.ScheduledRunOutcome(req.RunID)
			if !outcome.Terminal || outcome.ThreadID != req.ThreadID || outcome.TurnID != "scheduled-turn" || outcome.ResultText != "我会继续检查。" {
				t.Fatalf("terminal protocol evidence missing: %#v", outcome)
			}
			if (status == "completed") != (outcome.TerminalCause == "completed") || (status == "interrupted" && outcome.TerminalCause != "user_interrupted") {
				t.Fatalf("incorrect success/interruption classification: %#v", outcome)
			}
			if status == "eligible_failure" && (outcome.TerminalCause != "autocontinue_eligible_failure" || outcome.ResultError != problem.Message) {
				t.Fatalf("eligible failure evidence was not preserved: %#v", outcome)
			}
			if len(surface.QueueItems) != 1 || surface.AutoContinue.Episode != nil || surface.AutoWhip.PendingReason != "" || surface.ActiveQueueItemID != "" || result.QueueItemID == "" {
				t.Fatalf("scheduled task spawned a surface retry/continuation: %#v", surface)
			}
		})
	}
}

func TestScheduledAbortBeforeDispatchDoesNotLaunchNextQueuedMessage(t *testing.T) {
	for _, busy := range []bool{false, true} {
		s, req := scheduledTestSetup(t)
		if busy {
			s.Instance("inst-1").ActiveTurnID = "local-turn"
		}
		result := s.TryEnqueueScheduledPrompt(req)
		surface := s.Surface(req.SurfaceID)
		next := &state.QueueItemRecord{Status: state.QueueItemQueued, Inputs: []agentproto.Input{{Type: agentproto.InputText, Text: "later"}}, FrozenDispatchPlan: agentproto.DefaultPromptDispatchPlanForExecutionThread("thread-2")}
		s.enqueuePreparedQueueItem(surface, next, false)
		if !s.AbortScheduledRunBeforeDispatch(req.RunID) {
			t.Fatalf("unaccepted enqueue could not be rolled back; busy=%v result=%#v", busy, result)
		}
		outcome := s.ScheduledRunOutcome(req.RunID)
		if !outcome.Terminal || outcome.TerminalCause != "dispatch_failed" || surface.ActiveQueueItemID != "" || next.Status != state.QueueItemQueued || len(surface.QueuedQueueItemIDs) != 1 || surface.QueuedQueueItemIDs[0] != next.ID {
			t.Fatalf("rollback affected other work: outcome=%#v surface=%#v", outcome, surface)
		}
		if binding := s.pendingRemoteBinding("inst-1", ""); binding != nil {
			t.Fatalf("rollback left a pending writer reservation: %#v", binding)
		}
	}
}

func TestScheduledAbortRefusesWireAcceptedOrRunningTurn(t *testing.T) {
	for _, phase := range []string{"wire", "running"} {
		t.Run(phase, func(t *testing.T) {
			s, req := scheduledTestSetup(t)
			result := s.TryEnqueueScheduledPrompt(req)
			if phase == "wire" {
				s.pendingRemoteBinding("inst-1", "").CommandID = "sent-command"
			} else {
				s.ApplyAgentEvent("inst-1", agentproto.Event{Kind: agentproto.EventTurnStarted, ThreadID: req.ThreadID, TurnID: "running-turn", Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: req.SurfaceID}})
			}
			if s.AbortScheduledRunBeforeDispatch(req.RunID) || s.ScheduledRunOutcome(req.RunID).Terminal || s.Surface(req.SurfaceID).ActiveQueueItemID != result.QueueItemID {
				t.Fatal("rollback destroyed an already accepted command")
			}
			if instanceID := s.ScheduledDispatchInstanceID(req.SurfaceID); instanceID != "inst-1" {
				t.Fatalf("exact scheduled writer helper failed: %q", instanceID)
			}
		})
	}
}

func TestScheduledDispatchRejectedCapturesTerminalFailure(t *testing.T) {
	s, req := scheduledTestSetup(t)
	result := s.TryEnqueueScheduledPrompt(req)
	item := s.Surface(req.SurfaceID).QueueItems[result.QueueItemID]
	s.failSurfaceActiveQueueItem(s.Surface(req.SurfaceID), item, nil, false)
	if outcome := s.ScheduledRunOutcome(req.RunID); !outcome.Terminal || outcome.TerminalCause != "dispatch_failed" || outcome.ResultError == "" {
		t.Fatalf("rejection lost terminal evidence: %#v", outcome)
	}
}

func TestScheduledBufferedCommandRejectedAfterAbortAndOrdinaryDispatch(t *testing.T) {
	s, req := scheduledTestSetup(t)
	result := s.TryEnqueueScheduledPrompt(req)
	command := findAgentCommand(result.Events, agentproto.CommandPromptSend)
	if command == nil || command.CommandID != "relay-schedule-"+req.RunID || s.ScheduledCommandInstanceID(command.CommandID) != "inst-1" {
		t.Fatalf("scheduled command lacks immutable dispatch identity: %#v", command)
	}
	if !s.AbortScheduledRunBeforeDispatch(req.RunID) {
		t.Fatal("failed to cancel the unbound buffered command")
	}
	surface := s.Surface(req.SurfaceID)
	next := &state.QueueItemRecord{Status: state.QueueItemQueued, Inputs: []agentproto.Input{{Type: agentproto.InputText, Text: "ordinary"}}, FrozenDispatchPlan: agentproto.DefaultPromptDispatchPlanForExecutionThread("thread-2")}
	s.enqueuePreparedQueueItem(surface, next, false)
	if s.ScheduledCommandInstanceID(command.CommandID) != "" || surface.ActiveQueueItemID != next.ID || next.Status != state.QueueItemDispatching {
		t.Fatal("stale cancelled command rebound to ordinary work")
	}
}

func TestScheduledBufferedCommandRejectsChangedAttachment(t *testing.T) {
	s, req := scheduledTestSetup(t)
	result := s.TryEnqueueScheduledPrompt(req)
	command := findAgentCommand(result.Events, agentproto.CommandPromptSend)
	s.Surface(req.SurfaceID).AttachedInstanceID = "future-attachment"
	if s.ScheduledCommandInstanceID(command.CommandID) != "" {
		t.Fatal("buffered command followed a future attachment")
	}
	if outcome := s.ScheduledRunOutcome(req.RunID); !outcome.Terminal || outcome.TerminalCause != "dispatch_failed" || outcome.InstanceID != "inst-1" {
		t.Fatalf("mutated destination did not terminate the unsent run: %#v", outcome)
	}
}

func TestScheduledBufferedCommandRequeuesLateClaimOrLocalTurn(t *testing.T) {
	for _, reason := range []string{"claim", "local_turn", "offline"} {
		t.Run(reason, func(t *testing.T) {
			s, req := scheduledTestSetup(t)
			result := s.TryEnqueueScheduledPrompt(req)
			command := findAgentCommand(result.Events, agentproto.CommandPromptSend)
			if reason == "claim" {
				s.MaterializeSurface("other-owner", "app-1", "chat-2", "user-1")
				owner := s.Surface("other-owner")
				owner.AttachedInstanceID, owner.SelectedThreadID = "inst-1", "thread-1"
				s.threadClaims["thread-1"] = &threadClaimRecord{SurfaceSessionID: owner.SurfaceSessionID, InstanceID: "inst-1"}
			} else if reason == "local_turn" {
				s.Instance("inst-1").ActiveTurnID = "native-turn"
			} else {
				s.Instance("inst-1").Online = false
			}
			if s.ScheduledCommandInstanceID(command.CommandID) != "" {
				t.Fatal("late writer contention dispatched a buffered command")
			}
			item := s.Surface(req.SurfaceID).QueueItems[result.QueueItemID]
			if item.Status != state.QueueItemQueued || item.TerminalCause != "" || s.Surface(req.SurfaceID).ActiveQueueItemID != "" {
				t.Fatalf("temporary contention did not restore the unsent queue item: %#v", item)
			}
		})
	}
}

func TestScheduledBufferedCommandRejectsWireBoundDuplicate(t *testing.T) {
	s, req := scheduledTestSetup(t)
	result := s.TryEnqueueScheduledPrompt(req)
	command := findAgentCommand(result.Events, agentproto.CommandPromptSend)
	if s.ScheduledCommandInstanceID(command.CommandID) != "inst-1" {
		t.Fatal("missing original writer")
	}
	s.BindPendingRemoteCommand(req.SurfaceID, command.CommandID)
	if s.ScheduledCommandInstanceID(command.CommandID) != "" || s.ScheduledRunOutcome(req.RunID).Terminal {
		t.Fatal("wire-bound duplicate was resent or original run invalidated")
	}
}
