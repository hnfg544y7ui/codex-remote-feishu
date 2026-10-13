package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestAsyncQuestionIsVisibleBeforeTurnEndsAndDoesNotCaptureInput(t *testing.T) {
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	svc, surface := idleLocalNoticeFixture(t, &now)
	surface.Verbosity = state.SurfaceVerbosityQuiet
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: "surface", ChatID: "chat", ActorUserID: "owner", MessageID: "start", Text: "检查项目"})
	svc.ApplyAgentEvent("local", agentproto.Event{Kind: agentproto.EventTurnStarted, ThreadID: "thread", TurnID: "turn", Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: "surface"}})
	active := surface.ActiveQueueItemID
	event := agentproto.Event{Kind: agentproto.EventItemCompleted, ThreadID: "thread", TurnID: "turn", ItemID: "question", ItemKind: "agent_message",
		Metadata: map[string]any{"text": "是否允许升级？", "messageDelivery": "async", "hasAsyncQuestions": true}}
	events := svc.ApplyAgentEvent("local", event)
	visible := false
	for _, result := range events {
		if result.Block != nil && strings.Contains(result.Block.Text, "是否允许升级？") {
			visible = true
			if result.Block.Final || result.SurfaceSessionID != "surface" {
				t.Fatal("async question ended the turn or escaped its surface")
			}
		}
	}
	if !visible || surface.ActiveQueueItemID != active || len(surface.PendingRequests) != 0 || surface.ActiveRequestCapture != nil {
		t.Fatal("question was buffered or introduced a false request gate")
	}
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: "surface", ChatID: "chat", ActorUserID: "owner", MessageID: "other-task", Text: "下一项任务"})
	other := surface.QueuedQueueItemIDs[0]
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: "surface", ChatID: "chat", ActorUserID: "owner", MessageID: "answer", Text: "暂不升级"})
	steer := svc.ApplySurfaceAction(control.Action{Kind: control.ActionReactionCreated, SurfaceSessionID: "surface", ChatID: "chat", ActorUserID: "owner", TargetMessageID: "answer", ReactionType: "ThumbsUp"})
	inserted := false
	for _, result := range steer {
		if result.Command != nil && result.Command.Kind == agentproto.CommandTurnSteer && result.Command.Target.TurnID == "turn" {
			inserted = true
			if len(result.Command.Prompt.Inputs) != 1 || result.Command.Prompt.Inputs[0].Text != "暂不升级" {
				t.Fatal("inserting an answer included unrelated queued work")
			}
		}
	}
	if !inserted || surface.ActiveQueueItemID != active {
		t.Fatal("answer cannot be inserted into the waiting turn")
	}
	if len(surface.QueuedQueueItemIDs) != 1 || surface.QueuedQueueItemIDs[0] != other {
		t.Fatal("the unrelated queued task was lost or reordered")
	}
	svc.ApplyAgentEvent("local", agentproto.Event{Kind: agentproto.EventItemCompleted, ThreadID: "thread", TurnID: "turn", ItemID: "final", ItemKind: "agent_message", Metadata: map[string]any{"text": "已按回答完成检查"}})
	completed := svc.ApplyAgentEvent("local", agentproto.Event{Kind: agentproto.EventTurnCompleted, ThreadID: "thread", TurnID: "turn", Status: "completed"})
	final := false
	for _, result := range completed {
		if result.Block != nil && result.Block.Final {
			final = true
			if result.Block.Text != "已按回答完成检查" {
				t.Fatal("the async question replaced the actual final response")
			}
		}
	}
	if !final {
		t.Fatal("the actual final response was blocked")
	}
}

func TestOrdinaryAsyncMessageStillUsesNormalFinalBuffer(t *testing.T) {
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	svc, _ := idleLocalNoticeFixture(t, &now)
	events := svc.ApplyAgentEvent("local", agentproto.Event{Kind: agentproto.EventItemCompleted, ThreadID: "thread", TurnID: "turn", ItemID: "text", ItemKind: "agent_message",
		Metadata: map[string]any{"text": "普通回复", "messageDelivery": "async"}})
	for _, event := range events {
		if event.Block != nil {
			t.Fatal("async text without questions bypassed normal buffering")
		}
	}
}
