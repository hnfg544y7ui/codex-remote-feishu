package orchestrator

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

func TestGoalStatusResumeButtonAvailability(t *testing.T) {
	for _, tc := range []struct {
		status string
		label  string
	}{
		{"active", ""},
		{"paused", "恢复"},
		{"blocked", "继续"},
		{"complete", ""},
		{"unknown", ""},
	} {
		t.Run(tc.status, func(t *testing.T) {
			svc, surfaceID, instanceID := goalInterlockTestSetup(t)
			goal := agentproto.CloneThreadGoalUpdate(svc.root.Instances[instanceID].Threads["thread-1"].ThreadGoal)
			goal.Status = tc.status
			page := goalStatusPage(svc.root.Surfaces[surfaceID], goal)
			resumeCount := 0
			for _, button := range page.RelatedButtons {
				if button.CommandText == "/goal resume" {
					resumeCount++
					if tc.label == "" || button.Label != tc.label {
						t.Fatalf("unexpected resume button for %s: %#v", tc.status, button)
					}
				}
			}
			if tc.label != "" && resumeCount != 1 {
				t.Fatalf("%s needs exactly one resume button, got %d", tc.status, resumeCount)
			}
		})
	}
}

func TestGoalBlockedRenderedContinueWaitsForBackendResult(t *testing.T) {
	for _, failure := range []bool{false, true} {
		name := "success"
		if failure {
			name = "rpc_error"
		}
		t.Run(name, func(t *testing.T) {
			svc, surfaceID, instanceID := goalInterlockTestSetup(t)
			thread := svc.root.Instances[instanceID].Threads["thread-1"]
			blocked := agentproto.CloneThreadGoalUpdate(thread.ThreadGoal)
			blocked.Status = "blocked"
			query := svc.ApplySurfaceAction(goalContextActionForTest(surfaceID, "user-1", "/goal"))
			get := findAgentCommand(query, agentproto.CommandThreadGoalGet)
			if get == nil {
				t.Fatal("expected Goal query")
			}
			status := svc.ApplyAgentEvent(instanceID, agentproto.Event{
				Kind: agentproto.EventThreadGoalCommandResult, CommandID: get.CommandID,
				ThreadID: "thread-1", ThreadGoal: blocked,
			})
			buttons := goalProjectedButtonsForTest(t, status)
			callback := goalRenderedCallbackForTest(t, surfaceID, "user-1", buttons["继续"])
			issued := svc.ApplySurfaceAction(callback)
			set := findAgentCommand(issued, agentproto.CommandThreadGoalSet)
			if set == nil || set.Target.ThreadID != "thread-1" || set.Goal.Status != "active" || set.Goal.Purpose != "user_control" {
				t.Fatalf("continue must resume the selected Goal: %#v", set)
			}
			for _, event := range issued {
				if event.Kind == eventcontract.KindAgentCommand && event.Command != set {
					t.Fatalf("continue must not issue another task: %#v", event.Command)
				}
			}
			if thread.ThreadGoal.Status != "blocked" {
				t.Fatal("button click must not claim Goal resumed before backend acknowledgement")
			}
			assertGoalFeedbackOperation(t, issued, true, "om-goal-card")
			result := agentproto.Event{
				Kind: agentproto.EventThreadGoalCommandResult, CommandID: set.CommandID, ThreadID: "thread-1",
			}
			if failure {
				result.ErrorMessage = "resume rejected"
			} else {
				result.ThreadGoal = agentproto.CloneThreadGoalUpdate(blocked)
				result.ThreadGoal.Status = "active"
			}
			completed := svc.ApplyAgentEvent(instanceID, result)
			page := findPageEvent(completed)
			if page == nil {
				t.Fatal("resume result must produce visible feedback")
			}
			assertGoalFeedbackOperation(t, completed, true, "om-goal-card")
			if failure {
				if thread.ThreadGoal.Status != "blocked" || page.StatusKind != "error" {
					t.Fatalf("failed resume must retain blocked Goal and show error: %#v", page)
				}
			} else {
				if thread.ThreadGoal.Status != "active" {
					t.Fatal("backend acknowledgement must update Goal status")
				}
				buttons = goalProjectedButtonsForTest(t, completed)
				if buttons["暂停"] == nil || buttons["继续"] != nil {
					t.Fatalf("active Goal must offer pause, not continue: %#v", buttons)
				}
			}
		})
	}
}
