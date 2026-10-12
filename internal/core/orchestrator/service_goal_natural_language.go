package orchestrator

import (
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

// Natural Goal controls are produced only by trusted human text ingress. They
// bypass the prompt queue like /goal; objectives are never parsed as slash args.
func (s *Service) handleNaturalGoalCommand(surface *state.SurfaceConsoleRecord, action control.Action, threadID string) []eventcontract.Event {
	intent := action.NaturalGoal
	parsed, ok := control.ParseFeishuNaturalGoalAction(action.Text)
	if strings.TrimSpace(action.ActorUserID) == "" || !ok || !sameNaturalGoalIntent(intent, parsed.NaturalGoal) {
		return []eventcontract.Event{s.pageEvent(surface, s.goalErrorPage(surface, "Goal 操作未执行", "未收到明确的用户 Goal 控制指令，请使用 `/goal`。"))}
	}
	switch intent.Operation {
	case "new", "edit":
		return s.issueGoalCommand(surface, threadID, "set", goalSetCommand(threadID, intent.Objective, intent.TokenBudget, "user_control"), action.MessageID)
	case "get":
		return s.issueGoalCommand(surface, threadID, "get", goalGetCommand(threadID, "user_control"), action.MessageID)
	case "pause":
		return s.issueGoalCommand(surface, threadID, "pause", goalStatusCommand(threadID, "paused", "user_control"), action.MessageID)
	case "resume":
		return s.issueGoalCommand(surface, threadID, "resume", goalStatusCommand(threadID, "active", "user_control"), action.MessageID)
	case "clear":
		s.captureGoalFingerprint(surface, threadID, "clear")
		page := s.goalClearConfirmPage(surface)
		return []eventcontract.Event{s.pageEvent(surface, s.boundGoalPage(surface, action.ActorUserID, threadID, "clear", page))}
	default:
		return []eventcontract.Event{s.pageEvent(surface, s.goalErrorPage(surface, "Goal 操作未执行", "不支持的 Goal 控制操作，请使用 `/goal`。"))}
	}
}

func sameNaturalGoalIntent(a, b *control.GoalIntent) bool {
	if a == nil || b == nil || a.Operation != b.Operation || a.Objective != b.Objective || (a.TokenBudget == nil) != (b.TokenBudget == nil) {
		return false
	}
	return a.TokenBudget == nil || *a.TokenBudget == *b.TokenBudget
}
