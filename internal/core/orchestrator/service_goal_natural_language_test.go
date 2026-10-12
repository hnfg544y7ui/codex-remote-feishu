package orchestrator

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

func naturalGoalTestAction(t *testing.T, surfaceID, text string) control.Action {
	t.Helper()
	action, ok := control.ParseFeishuNaturalGoalAction(text)
	if !ok {
		t.Fatalf("not recognized: %q", text)
	}
	action.SurfaceSessionID = surfaceID
	action.ChatID = "chat-1"
	action.ActorUserID = "user-1"
	action.MessageID = "om-natural-goal"
	return action
}

func TestNaturalGoalCommandsBypassPromptQueueDuringActiveTurn(t *testing.T) {
	for _, tc := range []struct {
		text   string
		kind   agentproto.CommandKind
		status string
	}{
		{"查看当前Goal状态", agentproto.CommandThreadGoalGet, ""},
		{"暂停当前Goal", agentproto.CommandThreadGoalSet, "paused"},
		{"恢复Goal", agentproto.CommandThreadGoalSet, "active"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			svc, surfaceID, instanceID := goalInterlockTestSetup(t)
			// This fixture already has a live active Goal turn. Controls must
			// remain direct RPCs, without inserting another model prompt.
			surface := svc.root.Surfaces[surfaceID]
			queueCount := len(surface.QueuedQueueItemIDs)
			events := svc.ApplySurfaceAction(naturalGoalTestAction(t, surfaceID, tc.text))
			cmd := findAgentCommand(events, tc.kind)
			if cmd == nil || cmd.Target.ThreadID != "thread-1" || cmd.Goal.Status != tc.status || cmd.Goal.Purpose != "user_control" {
				t.Fatalf("wrong control: %#v", events)
			}
			if len(surface.QueuedQueueItemIDs) != queueCount {
				t.Fatal("control entered prompt queue")
			}
			if findAgentCommand(events, agentproto.CommandPromptSend) != nil {
				t.Fatal("control started a model prompt")
			}
			response := svc.ApplyAgentEvent(instanceID, agentproto.Event{Kind: agentproto.EventThreadGoalCommandResult, CommandID: cmd.CommandID, ThreadID: "thread-1", ThreadGoal: &agentproto.ThreadGoalUpdate{ThreadID: "thread-1", Objective: "existing", Status: "paused"}})
			if findPageEvent(response) == nil {
				t.Fatal("missing control response while turn active")
			}
		})
	}
}

func TestNaturalGoalObjectiveAndBudgetRemainTyped(t *testing.T) {
	for _, tc := range []struct {
		text, objective string
		budget          *int64
	}{
		{"创建Goal：检查 --budget 999 参数兼容性", "检查 --budget 999 参数兼容性", nil},
		{"创建Goal：完成登录重构，预算5万tokens", "完成登录重构", ptrInt64(50000)},
		{"把当前Goal改为：修复所有测试", "修复所有测试", nil},
	} {
		t.Run(tc.text, func(t *testing.T) {
			svc, surfaceID, _ := goalInterlockTestSetup(t)
			action := naturalGoalTestAction(t, surfaceID, tc.text)
			events := svc.ApplySurfaceAction(action)
			cmd := findAgentCommand(events, agentproto.CommandThreadGoalSet)
			if cmd == nil || cmd.Goal.Objective != tc.objective || (cmd.Goal.TokenBudget == nil) != (tc.budget == nil) {
				t.Fatalf("objective/budget changed: %#v", cmd)
			}
			if tc.budget != nil && *cmd.Goal.TokenBudget != *tc.budget {
				t.Fatalf("budget changed: %d", *cmd.Goal.TokenBudget)
			}
		})
	}
}

func TestNaturalGoalRejectsUnclaimedOrMissingThreadAndForgedIntent(t *testing.T) {
	for _, scenario := range []string{"foreign claim", "missing thread", "empty actor", "forged operation", "forged budget"} {
		t.Run(scenario, func(t *testing.T) {
			svc, surfaceID, instanceID := goalInterlockTestSetup(t)
			action := naturalGoalTestAction(t, surfaceID, "创建Goal：明确目标")
			switch scenario {
			case "foreign claim":
				delete(svc.threadClaims, "thread-1")
			case "missing thread":
				delete(svc.root.Instances[instanceID].Threads, "thread-1")
			case "empty actor":
				action.ActorUserID = ""
			case "forged operation":
				action.NaturalGoal.Operation = "clear"
			case "forged budget":
				action.NaturalGoal.TokenBudget = ptrInt64(123)
			}
			for _, event := range svc.ApplySurfaceAction(action) {
				if event.Kind == eventcontract.KindAgentCommand {
					t.Fatalf("unsafe command issued: %#v", event.Command)
				}
			}
		})
	}
}

func TestNaturalGoalClearOnlyOpensConfirmationAndPreservesInterlock(t *testing.T) {
	svc, surfaceID, instanceID := goalInterlockTestSetup(t)
	lease := &GoalInterlockLease{InstanceID: instanceID, ThreadID: "thread-1", Phase: GoalInterlockDraining}
	svc.setGoalInterlockLease(lease)
	events := svc.ApplySurfaceAction(naturalGoalTestAction(t, surfaceID, "清除当前Goal"))
	if findAgentCommand(events, agentproto.CommandThreadGoalClear) != nil || lease.ExternalMutationSeen {
		t.Fatal("opening clear confirmation performed a mutation")
	}
	page := findPageEvent(events)
	if page == nil || page.Title != "清除 Goal" {
		t.Fatalf("missing confirmation: %#v", events)
	}
	action := control.Action{Kind: control.ActionGoalCommand, SurfaceSessionID: surfaceID, ChatID: "chat-1", ActorUserID: "user-1", Text: page.RelatedButtons[0].CommandText}
	if findAgentCommand(svc.ApplySurfaceAction(action), agentproto.CommandThreadGoalClear) == nil || !lease.ExternalMutationSeen {
		t.Fatal("confirmed clear did not issue RPC and revoke auto-resume")
	}
}

func TestNaturalGoalUserPauseRevokesAutoResumeButQueryDoesNot(t *testing.T) {
	for _, text := range []string{"查看当前Goal状态", "暂停当前Goal"} {
		svc, surfaceID, instanceID := goalInterlockTestSetup(t)
		lease := &GoalInterlockLease{InstanceID: instanceID, ThreadID: "thread-1", Phase: GoalInterlockPausePending, PauseCommandID: "existing-pause"}
		svc.setGoalInterlockLease(lease)
		svc.ApplySurfaceAction(naturalGoalTestAction(t, surfaceID, text))
		if lease.ExternalMutationSeen != (text == "暂停当前Goal") || lease.PauseCommandID != "existing-pause" {
			t.Fatalf("lost ownership/barrier: %#v", lease)
		}
	}
}

func TestGoalUserCommandResultRequiresOriginalInstanceAndThread(t *testing.T) {
	svc, surfaceID, instanceID := goalInterlockTestSetup(t)
	cmd := findAgentCommand(svc.ApplySurfaceAction(naturalGoalTestAction(t, surfaceID, "查看当前Goal状态")), agentproto.CommandThreadGoalGet)
	wrong := agentproto.Event{Kind: agentproto.EventThreadGoalCommandResult, CommandID: cmd.CommandID, ThreadID: "other-thread"}
	if len(svc.applyGoalUserCommandResult(instanceID, wrong)) != 0 {
		t.Fatal("accepted wrong thread result")
	}
	wrong.ThreadID = "thread-1"
	if len(svc.applyGoalUserCommandResult("other-instance", wrong)) != 0 {
		t.Fatal("accepted wrong instance result")
	}
	if _, ok := svc.goalUserCommands[cmd.CommandID]; !ok {
		t.Fatal("mismatched result consumed pending command")
	}
	if len(svc.applyGoalUserCommandResult(instanceID, wrong)) == 0 {
		t.Fatal("valid response lost")
	}
}

func TestGoalUserMismatchedResultDoesNotOverwriteSnapshot(t *testing.T) {
	svc, surfaceID, instanceID := goalInterlockTestSetup(t)
	cmd := findAgentCommand(svc.ApplySurfaceAction(naturalGoalTestAction(t, surfaceID, "查看当前Goal状态")), agentproto.CommandThreadGoalGet)
	before := svc.root.Instances[instanceID].Threads["thread-1"].ThreadGoal.Objective
	svc.ApplyAgentEvent(instanceID, agentproto.Event{Kind: agentproto.EventThreadGoalCommandResult, CommandID: cmd.CommandID, ThreadID: "wrong", ThreadGoal: &agentproto.ThreadGoalUpdate{ThreadID: "thread-1", Objective: "wrong", Status: "active"}})
	if svc.root.Instances[instanceID].Threads["thread-1"].ThreadGoal.Objective != before {
		t.Fatal("mismatched result corrupted snapshot")
	}
	if _, ok := svc.goalUserCommands[cmd.CommandID]; !ok {
		t.Fatal("pending command lost")
	}
}

func TestGoalUserLateResultDoesNotShowOriginalGoalAsSelected(t *testing.T) {
	svc, surfaceID, instanceID := goalInterlockTestSetup(t)
	cmd := findAgentCommand(svc.ApplySurfaceAction(naturalGoalTestAction(t, surfaceID, "查看当前Goal状态")), agentproto.CommandThreadGoalGet)
	svc.root.Surfaces[surfaceID].SelectedThreadID = "other-thread"
	events := svc.ApplyAgentEvent(instanceID, agentproto.Event{Kind: agentproto.EventThreadGoalCommandResult, CommandID: cmd.CommandID, ThreadID: "thread-1", ThreadGoal: &agentproto.ThreadGoalUpdate{ThreadID: "thread-1", Objective: "original goal", Status: "active"}})
	page := findPageEvent(events)
	if page == nil || page.Title != "原会话 Goal 操作已返回" || containsSectionLine(page, "original goal") {
		t.Fatalf("old result misrepresented current selection: %#v", page)
	}
	for _, button := range page.RelatedButtons {
		if button.CommandText != "/goal" {
			t.Fatalf("old response exposes mutation: %#v", button)
		}
	}
}
