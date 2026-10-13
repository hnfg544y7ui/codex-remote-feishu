package orchestrator

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestTopicSteerAllKeepsOtherTopicQueueAndActiveTurn(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		name := "rejected"
		if accepted {
			name = "accepted"
		}
		t.Run(name, func(t *testing.T) {
			s := newRoomWorkspaceTestService(t)
			a := topicQueueSurfaceForTest(s, "om_a", "inst-droid-a", "thread-droid-a", "/data/dl/droid")
			b := topicQueueSurfaceForTest(s, "om_b", "inst-web", "thread-web", "/data/dl/web")
			room := s.ensureFeishuRoomContextForSurface(a)
			limit := 2
			room.ConcurrencyLimit = &limit
			for _, surface := range []*state.SurfaceConsoleRecord{a, b} {
				if !s.claimThread(surface, s.root.Instances[surface.AttachedInstanceID], surface.SelectedThreadID) {
					t.Fatal("failed to establish topic conversation claim")
				}
				action := control.Action{Kind: control.ActionTextMessage, GatewayID: surface.GatewayID,
					SurfaceSessionID: surface.SurfaceSessionID, ChatID: surface.ChatID, ActorUserID: "ou_owner",
					MessageID: surface.FeishuTopicRootMessageID + "_start", Text: surface.SelectedThreadID + " start"}
				events := s.ApplySurfaceAction(action)
				if command := promptSendCommand(events); command == nil {
					t.Fatal("initial topic task did not dispatch")
				}
				s.ApplyAgentEvent(surface.AttachedInstanceID, agentproto.Event{Kind: agentproto.EventTurnStarted,
					ThreadID: surface.SelectedThreadID, TurnID: surface.SelectedThreadID + "_turn"})
				for _, suffix := range []string{"one", "two"} {
					action.MessageID = surface.FeishuTopicRootMessageID + suffix
					action.Text = surface.SelectedThreadID + " " + suffix
					s.ApplySurfaceAction(action)
				}
			}
			if len(a.QueuedQueueItemIDs) != 2 || len(b.QueuedQueueItemIDs) != 2 {
				t.Fatal("topics did not retain separate supplemental inputs")
			}
			aQueued := append([]string(nil), a.QueuedQueueItemIDs...)
			bQueued := append([]string(nil), b.QueuedQueueItemIDs...)
			events := s.ApplySurfaceAction(control.Action{Kind: control.ActionSteerAll, GatewayID: b.GatewayID,
				SurfaceSessionID: b.SurfaceSessionID, ChatID: b.ChatID, ActorUserID: "ou_owner"})
			var command *agentproto.Command
			for _, event := range events {
				if event.Command != nil && event.Command.Kind == agentproto.CommandTurnSteer {
					command = event.Command
					if event.SurfaceSessionID != b.SurfaceSessionID {
						t.Fatal("steer output routed to another topic")
					}
				}
			}
			if command == nil || command.Target.ThreadID != b.SelectedThreadID || command.Target.TurnID != b.SelectedThreadID+"_turn" {
				t.Fatal("steer did not target the selected topic's active turn")
			}
			if len(command.Prompt.Inputs) != 2 || command.Prompt.Inputs[0].Text != "thread-web one" || command.Prompt.Inputs[1].Text != "thread-web two" {
				t.Fatal("steer merged another topic's inputs")
			}
			s.BindPendingRemoteCommand(b.SurfaceSessionID, "topic-steer")
			if accepted {
				s.HandleCommandAccepted(b.AttachedInstanceID, agentproto.CommandAck{CommandID: "topic-steer", Accepted: true})
			} else {
				s.HandleCommandRejected(b.AttachedInstanceID, agentproto.CommandAck{CommandID: "topic-steer", Accepted: false})
			}
			for i, id := range aQueued {
				if a.QueuedQueueItemIDs[i] != id || a.QueueItems[id].Status != state.QueueItemQueued {
					t.Fatal("steer altered the other topic's queue")
				}
			}
			if s.root.Instances[a.AttachedInstanceID].ActiveTurnID != a.SelectedThreadID+"_turn" {
				t.Fatal("steer interrupted the other topic's task")
			}
			for i, id := range bQueued {
				if accepted && b.QueueItems[id].Status != state.QueueItemSteered {
					t.Fatal("accepted supplement was not marked steered")
				}
				if !accepted && (b.QueuedQueueItemIDs[i] != id || b.QueueItems[id].Status != state.QueueItemQueued) {
					t.Fatal("rejected steer did not restore original queue order")
				}
			}
		})
	}
}
