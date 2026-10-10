package orchestrator

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestScheduledPendingIgnoresUncorrelatedOldTurnEvents(t *testing.T) {
	for _, kind := range []agentproto.EventKind{agentproto.EventTurnStarted, agentproto.EventTurnCompleted} {
		for _, initiator := range []agentproto.Initiator{
			{Kind: agentproto.InitiatorLocalUI},
			{Kind: agentproto.InitiatorUnknown},
			{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: "surface-1"},
		} {
			// A same-surface turn/start is the supported command-less acceptance.
			if kind == agentproto.EventTurnStarted && initiator.Kind == agentproto.InitiatorRemoteSurface {
				continue
			}
			t.Run(string(kind)+"/"+string(initiator.Kind), func(t *testing.T) {
				s, req := scheduledTestSetup(t)
				result := s.TryEnqueueScheduledPrompt(req)
				command := findAgentCommand(result.Events, agentproto.CommandPromptSend)
				s.BindPendingRemoteCommand(req.SurfaceID, command.CommandID)
				s.ApplyAgentEvent("inst-1", agentproto.Event{Kind: kind, ThreadID: req.ThreadID, TurnID: "old-turn", Status: "completed", Initiator: initiator})
				if item := s.Surface(req.SurfaceID).QueueItems[result.QueueItemID]; item.Status != state.QueueItemDispatching || item.TerminalCause != "" {
					t.Fatalf("old turn consumed a pending scheduled run: %#v", item)
				}
				if s.ScheduledRunOutcome(req.RunID).Terminal || s.pendingRemoteBinding("inst-1", req.ThreadID) == nil || s.turns.activeRemoteBinding("inst-1") != nil {
					t.Fatal("old turn changed the scheduled writer reservation")
				}
			})
		}
	}
}

func TestScheduledPendingRequiresMatchingCommandOrAttributedStart(t *testing.T) {
	for _, attribution := range []string{"command", "surface", "wrong-command", "wrong-surface", "wrong-thread"} {
		t.Run(attribution, func(t *testing.T) {
			s, req := scheduledTestSetup(t)
			result := s.TryEnqueueScheduledPrompt(req)
			command := findAgentCommand(result.Events, agentproto.CommandPromptSend)
			s.BindPendingRemoteCommand(req.SurfaceID, command.CommandID)
			event := agentproto.Event{Kind: agentproto.EventTurnStarted, ThreadID: req.ThreadID, TurnID: "actual-turn"}
			switch attribution {
			case "command":
				event.CommandID = command.CommandID
			case "surface":
				event.Initiator = agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: req.SurfaceID}
			case "wrong-command":
				event.CommandID = "previous-command"
				event.Initiator = agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: req.SurfaceID}
			case "wrong-surface":
				event.Initiator = agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: "another-surface"}
			case "wrong-thread":
				event.Initiator = agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: req.SurfaceID}
				event.ThreadID = "thread-2"
			}
			s.ApplyAgentEvent("inst-1", event)
			item := s.Surface(req.SurfaceID).QueueItems[result.QueueItemID]
			accepted := attribution == "command" || attribution == "surface"
			if accepted != (item.Status == state.QueueItemRunning) {
				t.Fatalf("incorrect pending event correlation: %#v", item)
			}
			if accepted {
				// Once bound, an older turn or terminal event without a TurnID
				// cannot end the run, even if its command field was copied.
				for _, turnID := range []string{"old-turn", ""} {
					s.ApplyAgentEvent("inst-1", agentproto.Event{Kind: agentproto.EventTurnCompleted, ThreadID: req.ThreadID, TurnID: turnID, CommandID: command.CommandID, Status: "completed"})
					if s.ScheduledRunOutcome(req.RunID).Terminal || item.Status != state.QueueItemRunning {
						t.Fatal("incorrect TurnID ended an active scheduled run")
					}
				}
				s.ApplyAgentEvent("inst-1", agentproto.Event{Kind: agentproto.EventTurnCompleted, ThreadID: req.ThreadID, TurnID: "actual-turn", Status: "completed"})
				if outcome := s.ScheduledRunOutcome(req.RunID); !outcome.Terminal || outcome.TerminalCause != "completed" || outcome.TurnID != "actual-turn" {
					t.Fatalf("actual terminal event was lost: %#v", outcome)
				}
			}
		})
	}
}

func TestScheduledMatchingRejectedStartPreservesNewDurableThread(t *testing.T) {
	s, req := scheduledTestSetup(t)
	req.Mode, req.ThreadID = "new_each_run", ""
	result := s.TryEnqueueScheduledPrompt(req)
	command := findAgentCommand(result.Events, agentproto.CommandPromptSend)
	s.BindPendingRemoteCommand(req.SurfaceID, command.CommandID)
	s.ApplyAgentEvent("inst-1", agentproto.Event{Kind: agentproto.EventThreadDiscovered, ThreadID: "new-durable-thread", CWD: req.Workspace, CommandID: command.CommandID})
	if item := s.Surface(req.SurfaceID).QueueItems[result.QueueItemID]; item.Status != state.QueueItemDispatching {
		t.Fatalf("discovery prematurely started a turn: %#v", item)
	}
	s.ApplyAgentEvent("inst-1", agentproto.Event{Kind: agentproto.EventTurnCompleted, ThreadID: "new-durable-thread", CommandID: command.CommandID, Status: "failed", ErrorMessage: "model rejected", TurnCompletionOrigin: agentproto.TurnCompletionOriginTurnStartRejected})
	outcome := s.ScheduledRunOutcome(req.RunID)
	if !outcome.Terminal || outcome.TerminalCause != "startup_failed" || outcome.ResultError != "model rejected" || outcome.ThreadID != "new-durable-thread" {
		t.Fatalf("matching start rejection lost its terminal evidence: %#v", outcome)
	}
	if s.Instance("inst-1").Threads["new-durable-thread"] == nil || s.Surface(req.SurfaceID).SelectedThreadID != "thread-2" {
		t.Fatal("new durable thread was lost or changed the chat selection")
	}
}
