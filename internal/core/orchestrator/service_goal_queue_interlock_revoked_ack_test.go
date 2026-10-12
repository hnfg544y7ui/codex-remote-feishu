package orchestrator

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestGoalUserMutationAlreadyIssuedResumeKeepsItsOwnAcknowledgement(t *testing.T) {
	svc, lease := userMutationLease(t, GoalInterlockResumePending)
	lease.ResumeCommandID = "interlock-resume-already-sent"
	key := goalInterlockKey(lease.InstanceID, lease.ThreadID)
	svc.goalInterlockByCommand[lease.ResumeCommandID] = key
	svc.revokeGoalAutoResumeForUserMutation(lease.InstanceID, lease.ThreadID)
	lateGet := svc.ApplyAgentEvent(lease.InstanceID, agentproto.Event{
		Kind:      agentproto.EventThreadGoalCommandResult,
		CommandID: lease.GetCommandID, ThreadID: lease.ThreadID,
		ThreadGoal: &agentproto.ThreadGoalUpdate{ThreadID: lease.ThreadID, Objective: lease.Objective, Status: "paused"},
	})
	if findAgentCommand(lateGet, agentproto.CommandThreadGoalSet) != nil || findAgentCommand(lateGet, agentproto.CommandThreadRead) != nil {
		t.Fatalf("late get duplicated or bypassed in-flight resume: %+v", lateGet)
	}
	if lease.Phase != GoalInterlockResumePending || svc.goalInterlockByCommand[lease.ResumeCommandID] != key {
		t.Fatal("revocation discarded an issued resume's acknowledgement")
	}
	ack := svc.ApplyAgentEvent(lease.InstanceID, agentproto.Event{
		Kind:      agentproto.EventThreadGoalCommandResult,
		CommandID: lease.ResumeCommandID, ThreadID: lease.ThreadID,
		ThreadGoal: &agentproto.ThreadGoalUpdate{ThreadID: lease.ThreadID, Objective: lease.Objective, Status: "active"},
	})
	if findAgentCommand(ack, agentproto.CommandThreadGoalSet) != nil {
		t.Fatalf("issued resume ack recreated auto-resume: %+v", ack)
	}
	if svc.goalInterlockLease(lease.InstanceID, lease.ThreadID) != nil {
		t.Fatal("completed issued resume was never cleaned up")
	}
}

func TestGoalUserMutationDrainingWaitsForQueueAndDeliveryBeforeRetiring(t *testing.T) {
	svc, lease := userMutationLease(t, GoalInterlockDraining)
	surface := svc.root.Surfaces[lease.TriggerSurfaceID]
	svc.revokeGoalAutoResumeForUserMutation(lease.InstanceID, lease.ThreadID)
	surface.ActiveQueueItemID = "prompt-awaiting-ack"
	if events := svc.maybeResumeGoalInterlock(lease.InstanceID, lease.ThreadID); len(events) != 0 {
		t.Fatalf("in-flight delivery emitted recovery commands: %+v", events)
	}
	if svc.goalInterlockLease(lease.InstanceID, lease.ThreadID) != lease {
		t.Fatal("in-flight delivery lost barrier")
	}
	surface.ActiveQueueItemID = ""
	surface.QueuedQueueItemIDs = []string{"queued-user-work"}
	svc.maybeResumeGoalInterlock(lease.InstanceID, lease.ThreadID)
	if svc.goalInterlockLease(lease.InstanceID, lease.ThreadID) != lease {
		t.Fatal("remaining user queue lost barrier")
	}
	surface.QueuedQueueItemIDs = nil
	if events := svc.maybeResumeGoalInterlock(lease.InstanceID, lease.ThreadID); len(events) != 0 {
		t.Fatalf("drained revoked lease resumed goal: %+v", events)
	}
	if svc.goalInterlockLease(lease.InstanceID, lease.ThreadID) != nil {
		t.Fatal("retired lease could block a future user-owned Goal")
	}
}

func TestGoalUserMutationFailedResumeGetAndFailedIdleProbeRemainFailClosed(t *testing.T) {
	svc, lease := userMutationLease(t, GoalInterlockResumePending)
	svc.revokeGoalAutoResumeForUserMutation(lease.InstanceID, lease.ThreadID)
	getFailure := svc.ApplyAgentEvent(lease.InstanceID, agentproto.Event{
		Kind:      agentproto.EventThreadGoalCommandResult,
		CommandID: lease.GetCommandID, ThreadID: lease.ThreadID, ErrorMessage: "get RPC failed",
	})
	probe := findAgentCommand(getFailure, agentproto.CommandThreadRead)
	if probe == nil || findAgentCommand(getFailure, agentproto.CommandThreadGoalSet) != nil {
		t.Fatalf("failed revoked get restored ownership: %+v", getFailure)
	}
	probeFailure := svc.ApplyAgentEvent(lease.InstanceID, agentproto.Event{
		Kind:      agentproto.EventThreadRuntimeStatusUpdated,
		CommandID: probe.CommandID, ThreadID: lease.ThreadID, ErrorMessage: "read RPC failed",
	})
	if findAgentCommand(probeFailure, agentproto.CommandPromptSend) != nil || findAgentCommand(probeFailure, agentproto.CommandThreadGoalSet) != nil {
		t.Fatalf("failed probe bypassed barrier: %+v", probeFailure)
	}
	if svc.goalInterlockLease(lease.InstanceID, lease.ThreadID) != lease || lease.Phase != GoalInterlockQuiescing || !lease.ExternalMutationSeen {
		t.Fatal("failed revoked recovery lost its barrier or no-resume marker")
	}
}
