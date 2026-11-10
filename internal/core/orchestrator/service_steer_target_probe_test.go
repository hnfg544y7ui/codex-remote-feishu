package orchestrator

import (
	"errors"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func lostSteerTargetFixture(t *testing.T) (*Service, *time.Time, *agentproto.Command) {
	t.Helper()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	svc := newSteerAllServiceFixture(&now)
	inst := svc.root.Instances["inst-1"]
	inst.ActiveThreadID, inst.ActiveTurnID = "", ""
	inst.Threads["thread-1"].RuntimeStatus = &agentproto.ThreadRuntimeStatus{Type: agentproto.ThreadRuntimeStatusTypeActive}
	svc.clearActiveRemoteTurn("inst-1")
	surface := svc.root.Surfaces["surface-1"]
	item := surface.QueueItems[surface.ActiveQueueItemID]
	item.Status = state.QueueItemDispatching
	binding := newRemoteTurnBindingForQueueItem(surface, inst, item)
	binding.CommandID = "unconfirmed-start"
	svc.bindPendingRemoteTurn("inst-1", binding)
	events := svc.ApplySurfaceAction(control.Action{Kind: control.ActionSteerAll, SurfaceSessionID: "surface-1", ActorUserID: "user-1"})
	probe := findAgentCommand(events, agentproto.CommandThreadHistoryRead)
	if probe == nil || probe.Target.ThreadID != "thread-1" || probe.HistoryRead == nil || !probe.HistoryRead.LatestTurnOnly {
		t.Fatalf("queued input without observed start needs bounded read, got %#v", events)
	}
	if findAgentCommand(events, agentproto.CommandTurnSteer) != nil || findAgentCommand(events, agentproto.CommandPromptSend) != nil {
		t.Fatal("query must not send or steer input before a target is confirmed")
	}
	return svc, &now, probe
}

func steerProbeResult(commandID string) agentproto.Event {
	return agentproto.Event{Kind: agentproto.EventThreadHistoryRead, CommandID: commandID, ThreadID: "thread-1",
		ThreadHistory: &agentproto.ThreadHistoryRecord{Thread: agentproto.ThreadSnapshotRecord{ThreadID: "thread-1", RuntimeStatus: &agentproto.ThreadRuntimeStatus{Type: agentproto.ThreadRuntimeStatusTypeActive}},
			Turns: []agentproto.ThreadHistoryTurnRecord{{TurnID: "observed-native-turn", Status: "inProgress"}}}}
}

func TestSteerAllResolvesLostTurnWithoutRebindingPendingPrompt(t *testing.T) {
	svc, _, probe := lostSteerTargetFixture(t)
	pending := svc.turns.pendingRemoteBinding("inst-1")
	events := svc.ApplyAgentEvent("inst-1", steerProbeResult(probe.CommandID))
	steer := findAgentCommand(events, agentproto.CommandTurnSteer)
	if steer == nil || steer.Target.ThreadID != "thread-1" || steer.Target.TurnID != "observed-native-turn" || len(steer.Prompt.Inputs) != 2 {
		t.Fatalf("expected one explicitly targeted steer for the original two inputs: %#v", events)
	}
	if svc.turns.pendingRemoteBinding("inst-1") != pending || pending.CommandID != "unconfirmed-start" || pending.TurnID != "" {
		t.Fatal("read-only target discovery must not adopt or replay an unconfirmed prompt")
	}
	if svc.root.Instances["inst-1"].ActiveTurnID != "" || svc.root.Surfaces["surface-1"].QueueItems["queue-1"].Status != state.QueueItemDispatching {
		t.Fatal("discovery must not invent turn ownership or change the dispatching input")
	}
	svc.BindPendingSteerCommand("surface-1", steer.CommandID)
	svc.HandleCommandAccepted("inst-1", agentproto.CommandAck{CommandID: steer.CommandID, Accepted: true})
	if pending.CommandID != "unconfirmed-start" {
		t.Fatal("steer acknowledgement replaced an unrelated pending prompt's identity")
	}
	for _, id := range []string{"queue-2", "queue-3"} {
		if svc.root.Surfaces["surface-1"].QueueItems[id].Status != state.QueueItemSteered {
			t.Fatal("steer acknowledgement did not confirm the input", id)
		}
	}
	if findAgentCommand(svc.ApplyAgentEvent("inst-1", steerProbeResult(probe.CommandID)), agentproto.CommandTurnSteer) != nil {
		t.Fatal("a duplicate query response repeated steering")
	}
}

func TestSteerAllTargetProbeFailsClosed(t *testing.T) {
	for _, failure := range []string{"thread", "instance", "actor", "claim", "metadata", "rpc", "complete", "not_loaded", "missing_status", "ambiguous", "timeout", "dispatch", "rejected", "offline"} {
		t.Run(failure, func(t *testing.T) {
			svc, now, probe := lostSteerTargetFixture(t)
			event := steerProbeResult(probe.CommandID)
			surface := svc.root.Surfaces["surface-1"]
			switch failure {
			case "thread":
				surface.SelectedThreadID = "thread-2"
			case "instance":
				surface.AttachedInstanceID = "inst-other"
			case "actor":
				surface.ActorUserID = "user-other"
			case "claim":
				delete(svc.threadClaims, "thread-1")
			case "metadata":
				event.ThreadHistory.Thread.ThreadID = "thread-2"
			case "rpc":
				event.ErrorMessage = "read failed"
			case "complete":
				event.ThreadHistory.Turns[0].Status = "completed"
			case "not_loaded":
				event.ThreadHistory.Thread.RuntimeStatus.Type = agentproto.ThreadRuntimeStatusTypeNotLoaded
			case "missing_status":
				event.ThreadHistory.Thread.RuntimeStatus = nil
			case "ambiguous":
				event.ThreadHistory.Turns = append(event.ThreadHistory.Turns, event.ThreadHistory.Turns[0])
			case "timeout":
				*now = now.Add(31 * time.Second)
			case "offline":
				svc.root.Instances["inst-1"].Online = false
			}
			var events []eventcontract.Event
			switch failure {
			case "dispatch":
				events = svc.HandleCommandDispatchFailure("surface-1", probe.CommandID, errors.New("send failed"))
			case "rejected":
				events = svc.HandleCommandRejected("inst-1", agentproto.CommandAck{CommandID: probe.CommandID})
			case "timeout", "offline":
				events = svc.Tick(*now)
			default:
				events = svc.ApplyAgentEvent("inst-1", event)
			}
			if len(events) == 0 || findAgentCommand(events, agentproto.CommandTurnSteer) != nil || len(svc.turns.steerTargetProbes) != 0 {
				t.Fatalf("failed probe must clear loading with feedback and never steer: %#v", events)
			}
			for _, id := range []string{"queue-2", "queue-3"} {
				if surface.QueueItems[id].Status != state.QueueItemQueued {
					t.Fatal("failed probe changed queued input", id)
				}
			}
		})
	}
}

func TestSteerAllTargetProbeKeepsNewAndWithdrawnInputsSeparate(t *testing.T) {
	svc, _, probe := lostSteerTargetFixture(t)
	duplicate := svc.ApplySurfaceAction(control.Action{Kind: control.ActionSteerAll, SurfaceSessionID: "surface-1", ActorUserID: "user-1"})
	if findAgentCommand(duplicate, agentproto.CommandThreadHistoryRead) != nil || len(svc.turns.steerTargetProbes) != 1 {
		t.Fatal("repeated steerall created concurrent reads")
	}
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionMessageRecalled, SurfaceSessionID: "surface-1", TargetMessageID: "msg-queued-1"})
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: "surface-1", ActorUserID: "user-1", MessageID: "new-during-probe", Text: "later input"})
	steer := findAgentCommand(svc.ApplyAgentEvent("inst-1", steerProbeResult(probe.CommandID)), agentproto.CommandTurnSteer)
	if steer == nil || len(steer.Prompt.Inputs) != 1 || steer.Prompt.Inputs[0].Text != "补充信息二" {
		t.Fatalf("probe must only steer surviving inputs selected by the original action: %#v", steer)
	}
	if surface := svc.root.Surfaces["surface-1"]; len(surface.QueuedQueueItemIDs) != 1 || surface.QueueItems[surface.QueuedQueueItemIDs[0]].SourceMessageID != "new-during-probe" {
		t.Fatal("newly arrived input was implicitly included in an older steer request")
	}
}

func TestSteerAllTargetProbeOwnerCardTimeoutSealsOriginal(t *testing.T) {
	svc, now, first := lostSteerTargetFixture(t)
	svc.failSteerTargetProbe(first.CommandID, "cancel fixture query")
	events := svc.ApplySurfaceAction(control.Action{Kind: control.ActionSteerAll,
		SurfaceSessionID: "surface-1", ActorUserID: "user-1", MessageID: "om-probe-card",
		CatalogFamilyID: control.FeishuCommandSteerAll, CatalogVariantID: "steerall.codex.normal",
		CatalogBackend: agentproto.BackendCodex, Inbound: &control.ActionInboundMeta{CardDaemonLifecycleID: "life-1"}})
	page := findPageEvent(events)
	if page == nil || page.MessageID != "om-probe-card" || page.Sealed || findAgentCommand(events, agentproto.CommandThreadHistoryRead) == nil {
		t.Fatal("menu query must keep progress on its source owner card", events)
	}
	*now = now.Add(31 * time.Second)
	page = findPageEvent(svc.Tick(*now))
	if page == nil || page.MessageID != "om-probe-card" || !page.Sealed {
		t.Fatal("timeout must terminate the original loading card", page)
	}
}

func TestSteerAllResolvedTurnRejectionRestoresInputsAndPendingIdentity(t *testing.T) {
	svc, _, probe := lostSteerTargetFixture(t)
	steer := findAgentCommand(svc.ApplyAgentEvent("inst-1", steerProbeResult(probe.CommandID)), agentproto.CommandTurnSteer)
	if steer == nil {
		t.Fatal("expected explicitly targeted steer")
	}
	svc.HandleCommandRejected("inst-1", agentproto.CommandAck{CommandID: steer.CommandID, Error: "expected turn is no longer active"})
	surface := svc.root.Surfaces["surface-1"]
	if len(surface.QueuedQueueItemIDs) != 2 || surface.QueuedQueueItemIDs[0] != "queue-2" || surface.QueuedQueueItemIDs[1] != "queue-3" {
		t.Fatal("rejected steer did not restore original queue order", surface.QueuedQueueItemIDs)
	}
	if svc.turns.pendingRemoteBinding("inst-1").CommandID != "unconfirmed-start" {
		t.Fatal("steer rejection touched the unrelated unconfirmed prompt")
	}
}
