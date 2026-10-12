package orchestrator

import (
	"errors"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func userMutationLease(t *testing.T, phase GoalInterlockPhase) (*Service, *GoalInterlockLease) {
	t.Helper()
	svc, surfaceID, instanceID := goalInterlockTestSetup(t)
	lease := &GoalInterlockLease{
		InstanceID: instanceID, ThreadID: "thread-1", Phase: phase,
		TriggerSurfaceID: surfaceID, Objective: "ship it",
		PauseCommandID: "interlock-pause", ProbeCommandID: "interlock-probe",
		GetCommandID: "interlock-get",
	}
	svc.RestoreGoalInterlockLeases([]GoalInterlockLease{*lease})
	return svc, svc.goalInterlockLease(instanceID, "thread-1")
}

func TestGoalUserMutationRevokesOnlyResumeAuthorizationInEveryPhase(t *testing.T) {
	for _, phase := range []GoalInterlockPhase{GoalInterlockPausePending, GoalInterlockQuiescing, GoalInterlockDraining, GoalInterlockResumePending} {
		t.Run(string(phase), func(t *testing.T) {
			svc, lease := userMutationLease(t, phase)
			svc.root.Surfaces[lease.TriggerSurfaceID].QueuedQueueItemIDs = []string{"pending-work"}
			key := goalInterlockKey(lease.InstanceID, lease.ThreadID)
			svc.revokeGoalAutoResumeForUserMutation(lease.InstanceID, lease.ThreadID)
			if svc.goalInterlockLease(lease.InstanceID, lease.ThreadID) != lease || lease.Phase != phase || !lease.ExternalMutationSeen {
				t.Fatalf("revocation bypassed barrier: %+v", lease)
			}
			if lease.PauseCommandID != "interlock-pause" || lease.ProbeCommandID != "interlock-probe" || lease.GetCommandID != "interlock-get" {
				t.Fatalf("command ownership lost: %+v", lease)
			}
			if svc.goalInterlockByCommand[lease.PauseCommandID] != key || svc.goalProbeByCommand[lease.ProbeCommandID] != key || svc.goalGetByCommand[lease.GetCommandID] != key {
				t.Fatal("pending acknowledgements discarded")
			}
			if events := svc.maybeResumeGoalInterlock(lease.InstanceID, lease.ThreadID); findAgentCommand(events, agentproto.CommandThreadGoalGet) != nil || findAgentCommand(events, agentproto.CommandThreadGoalSet) != nil {
				t.Fatalf("revoked owner resumed: %+v", events)
			}
		})
	}
}

func TestGoalUserMutationExactInstanceAndThread(t *testing.T) {
	svc, lease := userMutationLease(t, GoalInterlockDraining)
	otherThread := &GoalInterlockLease{InstanceID: lease.InstanceID, ThreadID: "thread-2", Phase: GoalInterlockDraining}
	otherInstance := &GoalInterlockLease{InstanceID: "inst-2", ThreadID: lease.ThreadID, Phase: GoalInterlockDraining}
	svc.setGoalInterlockLease(otherThread)
	svc.setGoalInterlockLease(otherInstance)
	for _, target := range [][2]string{{"unknown", "unknown"}, {"", "thread-1"}, {"inst-1", ""}, {"inst-unknown", "thread-1"}, {"inst-1", "unknown"}} {
		svc.revokeGoalAutoResumeForUserMutation(target[0], target[1])
	}
	if lease.ExternalMutationSeen || otherThread.ExternalMutationSeen || otherInstance.ExternalMutationSeen {
		t.Fatal("unknown target revoked an existing lease")
	}
	svc.revokeGoalAutoResumeForUserMutation(lease.InstanceID, lease.ThreadID)
	if !lease.ExternalMutationSeen || otherThread.ExternalMutationSeen || otherInstance.ExternalMutationSeen {
		t.Fatal("user mutation affected another thread or instance")
	}
}

func TestGoalUserMutationLatePauseAndProbeAcknowledgementsCannotRestorePermission(t *testing.T) {
	svc, lease := userMutationLease(t, GoalInterlockPausePending)
	svc.revokeGoalAutoResumeForUserMutation(lease.InstanceID, lease.ThreadID)
	pauseEvents := svc.ApplyAgentEvent(lease.InstanceID, agentproto.Event{Kind: agentproto.EventThreadGoalCommandResult, CommandID: lease.PauseCommandID, ThreadID: lease.ThreadID, ThreadGoal: &agentproto.ThreadGoalUpdate{ThreadID: lease.ThreadID, Objective: lease.Objective, Status: "paused"}})
	probe := findAgentCommand(pauseEvents, agentproto.CommandThreadRead)
	if probe == nil || !lease.ExternalMutationSeen || lease.Phase != GoalInterlockQuiescing {
		t.Fatalf("pause ack restored ownership: %+v %+v", lease, pauseEvents)
	}
	idleEvents := svc.ApplyAgentEvent(lease.InstanceID, agentproto.Event{Kind: agentproto.EventThreadRuntimeStatusUpdated, CommandID: probe.CommandID, ThreadID: lease.ThreadID, RuntimeStatus: &agentproto.ThreadRuntimeStatus{Type: agentproto.ThreadRuntimeStatusTypeIdle}})
	if findAgentCommand(idleEvents, agentproto.CommandThreadGoalGet) != nil || findAgentCommand(idleEvents, agentproto.CommandThreadGoalSet) != nil {
		t.Fatalf("idle ack resumed paused user goal: %+v", idleEvents)
	}
	if svc.goalInterlockLease(lease.InstanceID, lease.ThreadID) != nil {
		t.Fatal("empty revoked drain was never retired")
	}
}

func TestGoalUserMutationLateResumeGetRequiresIdleBarrier(t *testing.T) {
	svc, lease := userMutationLease(t, GoalInterlockResumePending)
	svc.revokeGoalAutoResumeForUserMutation(lease.InstanceID, lease.ThreadID)
	events := svc.ApplyAgentEvent(lease.InstanceID, agentproto.Event{Kind: agentproto.EventThreadGoalCommandResult, CommandID: lease.GetCommandID, ThreadID: lease.ThreadID, ThreadGoal: &agentproto.ThreadGoalUpdate{ThreadID: lease.ThreadID, Objective: lease.Objective, Status: "paused"}})
	if findAgentCommand(events, agentproto.CommandThreadGoalSet) != nil {
		t.Fatalf("stale get fingerprint restored authorization: %+v", events)
	}
	probe := findAgentCommand(events, agentproto.CommandThreadRead)
	if probe == nil || lease.Phase != GoalInterlockQuiescing || !lease.ExternalMutationSeen {
		t.Fatalf("resume cancellation dropped barrier: %+v %+v", lease, events)
	}
	if _, exists := svc.goalGetByCommand["interlock-get"]; exists {
		t.Fatal("acknowledged get retained as pending")
	}
	svc.ApplyAgentEvent(lease.InstanceID, agentproto.Event{Kind: agentproto.EventThreadRuntimeStatusUpdated, CommandID: probe.CommandID, ThreadID: lease.ThreadID, RuntimeStatus: &agentproto.ThreadRuntimeStatus{Type: agentproto.ThreadRuntimeStatusTypeActive}})
	if lease.Phase != GoalInterlockQuiescing {
		t.Fatal("active probe bypassed barrier")
	}
}

func TestGoalUserMutationRPCFailureDoesNotRestoreResumePermission(t *testing.T) {
	for _, dispatchFailure := range []bool{false, true} {
		svc, lease := userMutationLease(t, GoalInterlockDraining)
		surface := svc.root.Surfaces[lease.TriggerSurfaceID]
		events := svc.issueGoalCommand(surface, lease.ThreadID, "pause", goalStatusCommand(lease.ThreadID, "paused", "user_control"), "")
		command := findAgentCommand(events, agentproto.CommandThreadGoalSet)
		if command == nil {
			t.Fatal("expected user control command")
		}
		svc.revokeGoalAutoResumeForUserMutation(lease.InstanceID, lease.ThreadID)
		if dispatchFailure {
			svc.failGoalUserCommand(surface.SurfaceSessionID, command.CommandID, errors.New("delivery failed"))
		} else {
			svc.ApplyAgentEvent(lease.InstanceID, agentproto.Event{Kind: agentproto.EventThreadGoalCommandResult, CommandID: command.CommandID, ThreadID: lease.ThreadID, ErrorMessage: "RPC failed"})
		}
		if !lease.ExternalMutationSeen || svc.goalInterlockLease(lease.InstanceID, lease.ThreadID) != lease {
			t.Fatal("failed user control restored ownership or dropped barrier")
		}
		if events := svc.maybeResumeGoalInterlock(lease.InstanceID, lease.ThreadID); findAgentCommand(events, agentproto.CommandThreadGoalSet) != nil || findAgentCommand(events, agentproto.CommandThreadGoalGet) != nil {
			t.Fatalf("failure resumed goal: %+v", events)
		}
	}
}

func TestGoalQueryPreservesExistingInterlockResumeAuthorization(t *testing.T) {
	svc, lease := userMutationLease(t, GoalInterlockDraining)
	surface := svc.root.Surfaces[lease.TriggerSurfaceID]
	events := svc.issueGoalCommand(surface, lease.ThreadID, "get", goalGetCommand(lease.ThreadID, "user_control"), "")
	query := findAgentCommand(events, agentproto.CommandThreadGoalGet)
	svc.ApplyAgentEvent(lease.InstanceID, agentproto.Event{Kind: agentproto.EventThreadGoalCommandResult, CommandID: query.CommandID, ThreadID: lease.ThreadID, ThreadGoal: &agentproto.ThreadGoalUpdate{ThreadID: lease.ThreadID, Objective: lease.Objective, Status: "paused"}})
	if lease.ExternalMutationSeen {
		t.Fatal("read-only query revoked resume ownership")
	}
	resumeGet := findAgentCommand(svc.maybeResumeGoalInterlock(lease.InstanceID, lease.ThreadID), agentproto.CommandThreadGoalGet)
	if resumeGet == nil {
		t.Fatal("query prevented eventual resume")
	}
	result := svc.ApplyAgentEvent(lease.InstanceID, agentproto.Event{Kind: agentproto.EventThreadGoalCommandResult, CommandID: resumeGet.CommandID, ThreadID: lease.ThreadID, ThreadGoal: &agentproto.ThreadGoalUpdate{ThreadID: lease.ThreadID, Objective: lease.Objective, Status: "paused"}})
	resume := findAgentCommand(result, agentproto.CommandThreadGoalSet)
	if resume == nil || resume.Goal.Status != "active" {
		t.Fatalf("untouched interlock lost resume: %+v", result)
	}
}

func TestGoalUserMutationReconcileDoesNotPauseNewUserGoal(t *testing.T) {
	svc, lease := userMutationLease(t, GoalInterlockPausePending)
	svc.revokeGoalAutoResumeForUserMutation(lease.InstanceID, lease.ThreadID)
	svc.RestoreGoalInterlockLeases(svc.GoalInterlockLeases())
	events := svc.ReconcileGoalInterlocks()
	if findAgentCommand(events, agentproto.CommandThreadGoalSet) != nil {
		t.Fatalf("recovery paused the user's goal again: %+v", events)
	}
	if findAgentCommand(events, agentproto.CommandThreadRead) == nil {
		t.Fatalf("recovery lost idle probe: %+v", events)
	}
}

func TestGoalUserMutationLaterExternalNotificationKeepsQueueBarrier(t *testing.T) {
	svc, lease := userMutationLease(t, GoalInterlockQuiescing)
	svc.revokeGoalAutoResumeForUserMutation(lease.InstanceID, lease.ThreadID)
	svc.ApplyAgentEvent(lease.InstanceID, agentproto.Event{Kind: agentproto.EventThreadGoalUpdated, ThreadID: lease.ThreadID, ThreadGoal: &agentproto.ThreadGoalUpdate{ThreadID: lease.ThreadID, Status: "paused", ExternalMutation: true}})
	if svc.goalInterlockLease(lease.InstanceID, lease.ThreadID) != lease || lease.Phase != GoalInterlockQuiescing {
		t.Fatal("notification dropped user-mutated queue barrier")
	}
}
