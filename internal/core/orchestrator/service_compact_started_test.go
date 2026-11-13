package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func compactStartedFixture(t *testing.T, verbosity state.SurfaceVerbosity) *Service {
	t.Helper()
	now := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	svc := newCompactServiceFixture(&now)
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionUseThread, SurfaceSessionID: "surface-1", ThreadID: "thread-1"})
	svc.root.Surfaces["surface-1"].Verbosity = verbosity
	return svc
}

func compactLifecycleEvent(kind agentproto.EventKind) agentproto.Event {
	return agentproto.Event{Kind: kind, ThreadID: "thread-1", TurnID: "turn-1", ItemID: "compact-1", ItemKind: "context_compaction"}
}

func requireCompactProgress(t *testing.T, events []eventcontract.Event, summary, status string) *control.ExecCommandProgress {
	t.Helper()
	if len(events) != 1 || events[0].ExecCommandProgress == nil {
		t.Fatalf("expected one progress update, got %d events", len(events))
	}
	p := events[0].ExecCommandProgress
	if len(p.Timeline) != 1 || p.Timeline[0].ID != "compact-1" || p.Timeline[0].Kind != "context_compaction" || p.Timeline[0].Label != "压缩" || p.Timeline[0].Summary != summary || p.Timeline[0].Status != status {
		t.Fatalf("unexpected compaction timeline: %#v", p.Timeline)
	}
	return p
}

func TestContextCompactionStartedUpdatesSameProgressEntryUntilComplete(t *testing.T) {
	for _, verbosity := range []state.SurfaceVerbosity{state.SurfaceVerbosityNormal, state.SurfaceVerbosityVerbose} {
		t.Run(string(verbosity), func(t *testing.T) {
			svc := compactStartedFixture(t, verbosity)
			start := compactLifecycleEvent(agentproto.EventItemStarted)
			first := requireCompactProgress(t, svc.ApplyAgentEvent("inst-1", start), "正在压缩上下文…", "running")
			if len(first.Segments) != 1 {
				t.Fatal("start did not use normal progress segment")
			}
			svc.RecordExecCommandProgressSegment("surface-1", "thread-1", "turn-1", "compact-1", "om-compact-progress")
			if again := svc.ApplyAgentEvent("inst-1", start); len(again) != 0 {
				t.Fatal("duplicate start emitted another update")
			}
			completed := requireCompactProgress(t, svc.ApplyAgentEvent("inst-1", compactLifecycleEvent(agentproto.EventItemCompleted)), "上下文已压缩。", "completed")
			if activeProgressMessageID(completed) != "om-compact-progress" || completed.ActiveSegmentID != first.ActiveSegmentID || completed.Timeline[0].LastSeq != first.Timeline[0].LastSeq {
				t.Fatal("completion moved or duplicated the existing progress row/card")
			}
			if late := svc.ApplyAgentEvent("inst-1", start); len(late) != 0 {
				t.Fatal("late start regressed completed row")
			}
			entry := svc.root.Surfaces["surface-1"].ActiveExecProgress.Entries[0]
			if entry.Status != "completed" || entry.Summary != "上下文已压缩。" {
				t.Fatal("completed row was overwritten")
			}
		})
	}
}

func TestContextCompactionStartedPreservesQuietAndRequiresRealIDs(t *testing.T) {
	svc := compactStartedFixture(t, state.SurfaceVerbosityQuiet)
	if events := svc.ApplyAgentEvent("inst-1", compactLifecycleEvent(agentproto.EventItemStarted)); len(events) != 0 {
		t.Fatal("quiet start became visible")
	}
	if svc.root.Surfaces["surface-1"].ActiveExecProgress != nil {
		t.Fatal("quiet start created progress state")
	}
	for _, missing := range []string{"item", "thread", "turn"} {
		svc = compactStartedFixture(t, state.SurfaceVerbosityNormal)
		event := compactLifecycleEvent(agentproto.EventItemStarted)
		switch missing {
		case "item":
			event.ItemID = ""
		case "thread":
			event.ThreadID = ""
		case "turn":
			event.TurnID = ""
		}
		if events := svc.ApplyAgentEvent("inst-1", event); len(events) != 0 {
			t.Fatalf("missing %s identity created progress", missing)
		}
	}
}

func TestContextCompactionStartedDoesNotReplayWithoutSurface(t *testing.T) {
	svc := compactStartedFixture(t, state.SurfaceVerbosityNormal)
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionDetach, SurfaceSessionID: "surface-1"})
	if events := svc.ApplyAgentEvent("inst-1", compactLifecycleEvent(agentproto.EventItemStarted)); len(events) != 0 {
		t.Fatal("detached start emitted progress")
	}
	if svc.root.Instances["inst-1"].Threads["thread-1"].UndeliveredReplay != nil {
		t.Fatal("start stored stale replay notice")
	}
}

func TestManualContextCompactionStartedKeepsExistingOwnerCard(t *testing.T) {
	svc := compactStartedFixture(t, state.SurfaceVerbosityNormal)
	owner, _ := requireCompactStartEvents(t, svc.ApplySurfaceAction(control.Action{Kind: control.ActionCompact, SurfaceSessionID: "surface-1", ChatID: "chat-1", ActorUserID: "user-1"}))
	svc.RecordOwnerCardFlowMessage("surface-1", owner.TrackingKey, "om-manual-compact")
	svc.BindPendingRemoteCommand("surface-1", "cmd-manual-compact")
	svc.ApplyAgentEvent("inst-1", agentproto.Event{Kind: agentproto.EventTurnStarted, ThreadID: "thread-1", TurnID: "turn-1", Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: "surface-1"}})
	if events := svc.ApplyAgentEvent("inst-1", compactLifecycleEvent(agentproto.EventItemStarted)); len(events) != 0 {
		t.Fatal("manual start duplicated existing foreground owner card")
	}
	if svc.root.Surfaces["surface-1"].ActiveExecProgress != nil {
		t.Fatal("manual start created second progress card")
	}
	events := svc.ApplyAgentEvent("inst-1", compactLifecycleEvent(agentproto.EventItemCompleted))
	if len(events) != 1 {
		t.Fatal("manual completion lost its owner update")
	}
	done := commandCatalogFromEvent(t, events[0])
	if done.MessageID != "om-manual-compact" || done.Title != "上下文已压缩" {
		t.Fatal("manual completion changed owner target")
	}
}

func TestContextCompactionStartedSuppressesHelperTraffic(t *testing.T) {
	for _, initiator := range []bool{false, true} {
		svc := compactStartedFixture(t, state.SurfaceVerbosityNormal)
		e := compactLifecycleEvent(agentproto.EventItemStarted)
		if initiator {
			e.Initiator.Kind = agentproto.InitiatorInternalHelper
		} else {
			e.TrafficClass = agentproto.TrafficClassInternalHelper
		}
		if events := svc.ApplyAgentEvent("inst-1", e); len(events) != 0 {
			t.Fatal("helper compaction start leaked to user progress")
		}
		if svc.root.Surfaces["surface-1"].ActiveExecProgress != nil {
			t.Fatal("helper start mutated progress")
		}
	}
}

func TestContextCompactionStartedHasNoExecutionGateAndCleansUpOnTurnEnd(t *testing.T) {
	for _, status := range []string{"completed", "failed", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			svc := compactStartedFixture(t, state.SurfaceVerbosityNormal)
			startRemoteTurnForAutoWhipTest(t, svc, "source-task", "检查任务", "turn-1")
			surface := svc.root.Surfaces["surface-1"]
			requireCompactProgress(t, svc.ApplyAgentEvent("inst-1", compactLifecycleEvent(agentproto.EventItemStarted)), "正在压缩上下文…", "running")
			if svc.turns.compactTurns["inst-1"] != nil {
				t.Fatal("automatic progress created a manual compact gate")
			}
			events := svc.ApplyAgentEvent("inst-1", agentproto.Event{Kind: agentproto.EventTurnCompleted, ThreadID: "thread-1", TurnID: "turn-1", Status: status})
			if surface.ActiveExecProgress != nil {
				t.Fatal("ended turn retained active compaction progress")
			}
			for _, e := range events {
				if e.ExecCommandProgress != nil {
					for _, item := range e.ExecCommandProgress.Timeline {
						if item.Kind == "context_compaction" && item.Summary == "上下文已压缩。" {
							t.Fatal("turn end invented compaction completion")
						}
					}
				}
			}
		})
	}
}

func TestContextCompactionStartedDisconnectDoesNotCreateManualGate(t *testing.T) {
	svc := compactStartedFixture(t, state.SurfaceVerbosityNormal)
	requireCompactProgress(t, svc.ApplyAgentEvent("inst-1", compactLifecycleEvent(agentproto.EventItemStarted)), "正在压缩上下文…", "running")
	svc.ApplyInstanceDisconnected("inst-1")
	if svc.turns.compactTurns["inst-1"] != nil {
		t.Fatal("disconnect left a synthetic compaction gate")
	}
	if svc.root.Instances["inst-1"].Online {
		t.Fatal("disconnect did not mark the instance offline")
	}
	// Disconnect preserves the last observed generic progress snapshot. It is
	// not evidence of completion and must not create a manual execution gate.
	if progress := svc.root.Surfaces["surface-1"].ActiveExecProgress; progress != nil {
		for _, entry := range progress.Entries {
			if entry.Kind == "context_compaction" && (entry.Status == "completed" || entry.Summary == "上下文已压缩。") {
				t.Fatal("disconnect invented compaction completion")
			}
		}
	}
}
