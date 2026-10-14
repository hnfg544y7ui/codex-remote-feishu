package orchestrator

import (
	"errors"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

func TestGoalFeedbackOwnsOnlyStampedCommandCards(t *testing.T) {
	for _, text := range []string{
		"/goal", "/goal pause", "/goal resume", "/goal new 完成测试", "/goal edit 修复测试",
		"查看当前Goal状态", "继续执行目标", "暂停当前Goal", "创建Goal：完成测试",
	} {
		for _, ingress := range []string{"text", "text metadata", "stamped card"} {
			for _, outcome := range []string{"success", "backend error", "dispatch failure", "selection changed"} {
				t.Run(text+"/"+ingress+"/"+outcome, func(t *testing.T) {
					svc, surfaceID, instanceID := goalInterlockTestSetup(t)
					action := control.Action{Kind: control.ActionGoalCommand, SurfaceSessionID: surfaceID,
						ChatID: "chat-1", ActorUserID: "user-1", MessageID: "om-source", Text: text}
					if parsed, ok := control.ParseFeishuNaturalGoalAction(text); ok {
						action.NaturalGoal = parsed.NaturalGoal
					}
					owner := ""
					if ingress != "text" {
						action.Inbound = &control.ActionInboundMeta{LifecycleVerdict: control.InboundLifecycleCurrent}
					}
					if ingress == "stamped card" {
						resolved, ok := control.ResolveFeishuActionCatalog(svc.buildCatalogContext(svc.root.Surfaces[surfaceID]), action)
						if !ok {
							t.Fatal("missing current Goal command provenance")
						}
						action = resolved.Action
						action.Inbound.CardCallback = true
						action.Inbound.CardDaemonLifecycleID = "life-1"
						owner = action.MessageID
					}
					surface := svc.root.Surfaces[surfaceID]
					activeItem, queued := surface.ActiveQueueItemID, len(surface.QueuedQueueItemIDs)
					events := svc.ApplySurfaceAction(action)
					var command *agentproto.Command
					for _, event := range events {
						if event.Command != nil {
							command = event.Command
						}
					}
					if command == nil || command.Goal.Purpose != "user_control" {
						t.Fatalf("missing Goal control for %q; page=%v", text, findPageEvent(events))
					}
					if svc.goalUserCommands[command.CommandID].MessageID != owner {
						t.Fatal("user text captured as an async card owner")
					}
					loading := owner != "" && command.Kind != agentproto.CommandThreadGoalGet
					assertGoalFeedbackOperation(t, events, loading, owner)
					var result []eventcontract.Event
					if outcome == "dispatch failure" {
						result = svc.failGoalUserCommand(surfaceID, command.CommandID, errors.New("delivery failed"))
					} else {
						if outcome == "selection changed" {
							surface.SelectedThreadID = "thread-2"
						}
						reply := agentproto.Event{Kind: agentproto.EventThreadGoalCommandResult, CommandID: command.CommandID,
							ThreadID: "thread-1", ThreadGoal: &agentproto.ThreadGoalUpdate{ThreadID: "thread-1", Objective: "original", Status: "active"}}
						if outcome == "backend error" {
							reply.ErrorMessage = "Goal RPC failed"
						}
						result = svc.ApplyAgentEvent(instanceID, reply)
					}
					assertGoalFeedbackOperation(t, result, true, owner)
					if _, exists := svc.goalUserCommands[command.CommandID]; exists {
						t.Fatal("completed control retained pending owner")
					}
					if outcome == "selection changed" && findPageEvent(result).Title != "原会话 Goal 操作已返回" {
						t.Fatal("late result relabeled as current Goal")
					}
					if surface.ActiveQueueItemID != activeItem || len(surface.QueuedQueueItemIDs) != queued ||
						len(surface.PendingRequests) != 0 || surface.ActiveRequestCapture != nil {
						t.Fatal("feedback changed the running task or request gate")
					}
				})
			}
		}
	}
}

func assertGoalFeedbackOperation(t *testing.T, events []eventcontract.Event, expected bool, owner string) {
	t.Helper()
	count := 0
	for _, event := range events {
		if event.PageView == nil {
			continue
		}
		count++
		operations := feishu.NewProjector().ProjectEvent("chat-1", event)
		kind := feishu.OperationSendCard
		if owner != "" {
			kind = feishu.OperationUpdateCard
		}
		if len(operations) != 1 || operations[0].Kind != kind || operations[0].MessageID != owner {
			t.Fatalf("wrong Feishu delivery: want %s owner=%q, got %#v", kind, owner, operations)
		}
	}
	if (count == 1) != expected || count > 1 {
		t.Fatalf("expected feedback=%v, got %d pages", expected, count)
	}
}
