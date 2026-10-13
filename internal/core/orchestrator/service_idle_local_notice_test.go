package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func idleLocalNoticeFixture(t *testing.T, now *time.Time) (*Service, *state.SurfaceConsoleRecord) {
	t.Helper()
	s := newServiceForTest(now)
	workspace := t.TempDir()
	s.UpsertInstance(&state.InstanceRecord{InstanceID: "local", Online: true, WorkspaceRoot: workspace, WorkspaceKey: workspace,
		ObservedFocusedThreadID: "thread", Threads: map[string]*state.ThreadRecord{
			"thread": {ThreadID: "thread", CWD: workspace, Loaded: true},
		}})
	s.ApplySurfaceAction(control.Action{Kind: control.ActionAttachInstance, SurfaceSessionID: "surface", InstanceID: "local", ChatID: "chat", ActorUserID: "owner"})
	s.ApplySurfaceAction(control.Action{Kind: control.ActionUseThread, SurfaceSessionID: "surface", ThreadID: "thread"})
	return s, s.root.Surfaces["surface"]
}

func TestIdleLocalActivityIsSilentButStillProtectsDispatch(t *testing.T) {
	for _, kind := range []agentproto.EventKind{agentproto.EventLocalInteractionObserved, agentproto.EventTurnStarted} {
		for _, expiry := range []bool{false, true} {
			t.Run(string(kind)+map[bool]string{false: "/completed", true: "/watchdog"}[expiry], func(t *testing.T) {
				now := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
				s, surface := idleLocalNoticeFixture(t, &now)
				events := s.ApplyAgentEvent("local", agentproto.Event{Kind: kind, ThreadID: "thread", TurnID: "app-turn",
					Action: "turn_start", Initiator: agentproto.Initiator{Kind: agentproto.InitiatorLocalUI}})
				if len(events) != 0 || surface.DispatchMode != state.DispatchModePausedForLocal {
					t.Fatalf("idle activity must pause silently: mode=%s events=%v", surface.DispatchMode, len(events))
				}
				if expiry {
					now = now.Add(s.config.LocalPauseMaxWait + time.Second)
					events = s.Tick(now)
				} else {
					events = s.ApplyAgentEvent("local", agentproto.Event{Kind: agentproto.EventTurnCompleted, ThreadID: "thread",
						TurnID: "app-turn", Status: "completed", Initiator: agentproto.Initiator{Kind: agentproto.InitiatorLocalUI}})
				}
				if len(events) != 0 || surface.DispatchMode != state.DispatchModeNormal {
					t.Fatalf("idle activity must resume silently: mode=%s events=%v", surface.DispatchMode, len(events))
				}
			})
		}
	}
}

func TestLocalPauseNoticeRequiresAnActualWaitingQueueItem(t *testing.T) {
	for _, status := range []state.QueueItemStatus{"", state.QueueItemQueued, state.QueueItemCompleted, state.QueueItemFailed} {
		t.Run(string(status), func(t *testing.T) {
			now := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
			s, surface := idleLocalNoticeFixture(t, &now)
			surface.QueuedQueueItemIDs = []string{"missing", "item"}
			if status != "" {
				surface.QueueItems["item"] = &state.QueueItemRecord{ID: "item", Status: status}
			}
			events := s.ApplyAgentEvent("local", agentproto.Event{Kind: agentproto.EventLocalInteractionObserved, ThreadID: "thread", Action: "turn_start"})
			if (noticeCode(events, "local_activity_detected") != "") != (status == state.QueueItemQueued) {
				t.Fatalf("incorrect notification for queue status %q", status)
			}
			if surface.DispatchMode != state.DispatchModePausedForLocal {
				t.Fatal("notification suppression removed dispatch protection")
			}
			if duplicate := s.ApplyAgentEvent("local", agentproto.Event{Kind: agentproto.EventLocalInteractionObserved, ThreadID: "thread", Action: "turn_start"}); len(duplicate) != 0 {
				t.Fatal("duplicate local activity emitted another notice")
			}
		})
	}
}
