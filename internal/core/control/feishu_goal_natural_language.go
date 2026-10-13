package control

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

const (
	goalOperationNew    = "new"
	goalOperationEdit   = "edit"
	goalOperationGet    = "get"
	goalOperationPause  = "pause"
	goalOperationResume = "resume"
	goalOperationClear  = "clear"
)

// GoalIntent is the structured meaning of an explicit natural-language Goal
// control sentence. TokenBudget is nil when the user did not provide one.
type GoalIntent struct {
	Operation   string `json:"operation"`
	Objective   string `json:"objective,omitempty"`
	TokenBudget *int64 `json:"token_budget,omitempty"`
}

var (
	goalNewChinesePattern = regexp.MustCompile(`^(?:请)?(?:创建|新建)(?:一个)?(?:当前)?(?:Goal|goal|目标)\s*(?:[:：]|为)\s*(.+)$`)
	goalNewEnglishPattern = regexp.MustCompile(`(?i)^(?:please\s+)?(?:create|start|set\s+up)\s+(?:a\s+)?(?:new\s+)?goal\s*(?::|\bto\b)\s*(.+)$`)

	goalEditChineseSubjectPattern = regexp.MustCompile(`^(?:请)?(?:把|将)(?:当前)?(?:Goal|goal|目标)\s*(?:修改|改|更新)(?:为|成)\s*[:：]?\s*(.+)$`)
	goalEditChineseVerbPattern    = regexp.MustCompile(`^(?:请)?(?:修改|编辑|更新)(?:当前)?(?:Goal|goal|目标)\s*(?:[:：]|为)\s*(.+)$`)
	goalEditEnglishPattern        = regexp.MustCompile(`(?i)^(?:please\s+)?(?:edit|update|change)\s+(?:the\s+)?(?:current\s+)?goal\s*(?::|\bto\b)\s*(.+)$`)

	goalGetPattern    = regexp.MustCompile(`(?i)^(?:请)?(?:查看|查询)(?:一下)?(?:当前)?\s*(?:Goal|目标)(?:的)?(?:状态)?\s*[。.!！?？]?$|^(?:please\s+)?(?:show|view|check)\s+(?:the\s+)?current\s+goal(?:\s+status)?\s*[.!?]?$`)
	goalPausePattern  = regexp.MustCompile(`(?i)^(?:请)?暂停(?:当前)?\s*(?:Goal|目标)\s*[。.!！]?$|^(?:please\s+)?pause\s+(?:the\s+)?(?:current\s+)?goal\s*[.!]?$`)
	goalResumePattern = regexp.MustCompile(`(?i)^(?:请\s*)?(?:恢复|继续)(?:执行)?\s*(?:当前\s*)?(?:Goal|目标)(?:\s*的?\s*执行)?\s*[。.!！]?$|^(?:please\s+)?(?:resume|continue)\s+(?:the\s+)?(?:current\s+)?goal(?:\s+execution)?\s*[.!]?$`)
	goalClearPattern  = regexp.MustCompile(`(?i)^(?:请)?(?:清除|清空)(?:当前)?\s*(?:Goal|目标)\s*[。.!！]?$|^(?:please\s+)?clear\s+(?:the\s+)?(?:current\s+)?goal\s*[.!]?$`)

	goalBudgetPattern       = regexp.MustCompile(`(?i)(?:[,，;；]\s*|\s+)(?:预算|token(?:s)?\s*预算|token\s+budget|budget)\s*[:：=]?\s*([0-9]+)\s*(k|千|万)?\s*(?:个?\s*)?(?:tokens?|令牌)?\s*[。.!！]?$`)
	goalBudgetMarkerPattern = regexp.MustCompile(`(?i)(?:[,，;；]\s*|\s+)(?:预算|token(?:s)?\s*预算|token\s+budget|budget)`)
)

// ParseFeishuNaturalGoalAction recognizes only complete, explicit Goal control
// sentences. It is intentionally separate from the global slash-command parser
// and is safe to call only from a trusted Feishu text ingress.
func ParseFeishuNaturalGoalAction(text string) (Action, bool) {
	trimmed := strings.TrimSpace(text)
	if !naturalGoalCandidateAllowed(trimmed) {
		return Action{}, false
	}

	for _, candidate := range []struct {
		pattern   *regexp.Regexp
		operation string
	}{
		{goalNewChinesePattern, goalOperationNew},
		{goalNewEnglishPattern, goalOperationNew},
		{goalEditChineseSubjectPattern, goalOperationEdit},
		{goalEditChineseVerbPattern, goalOperationEdit},
		{goalEditEnglishPattern, goalOperationEdit},
	} {
		match := candidate.pattern.FindStringSubmatch(trimmed)
		if len(match) != 2 {
			continue
		}
		objective, budget, ok := parseNaturalGoalObjective(match[1])
		if !ok {
			return Action{}, false
		}
		return naturalGoalAction(text, GoalIntent{
			Operation:   candidate.operation,
			Objective:   objective,
			TokenBudget: budget,
		}), true
	}

	for _, candidate := range []struct {
		pattern   *regexp.Regexp
		operation string
	}{
		{goalGetPattern, goalOperationGet},
		{goalPausePattern, goalOperationPause},
		{goalResumePattern, goalOperationResume},
		{goalClearPattern, goalOperationClear},
	} {
		if candidate.pattern.MatchString(trimmed) {
			return naturalGoalAction(text, GoalIntent{Operation: candidate.operation}), true
		}
	}
	return Action{}, false
}

func naturalGoalAction(text string, intent GoalIntent) Action {
	return Action{
		Kind:        ActionGoalCommand,
		CommandID:   FeishuCommandGoal,
		Text:        text,
		NaturalGoal: &intent,
	}
}

func naturalGoalCandidateAllowed(text string) bool {
	if text == "" || strings.ContainsAny(text, "\r\n") || strings.Contains(text, "```") || strings.HasPrefix(text, "/") {
		return false
	}
	if strings.HasSuffix(text, "?") || strings.HasSuffix(text, "？") {
		// Exact status queries are handled below; mutation-like questions are
		// intentionally rejected before objective parsing.
		if !goalGetPattern.MatchString(text) {
			return false
		}
	}
	for _, prefix := range []string{"\"", "'", "`", "“", "‘", ">"} {
		if strings.HasPrefix(text, prefix) {
			return false
		}
	}
	lower := strings.ToLower(text)
	for _, prefix := range []string{
		"不要", "别", "不用", "无需", "不想", "如果", "假如", "假设", "假定", "要是",
		"don't ", "do not ", "dont ", "not ", "if ", "suppose ", "assuming ",
	} {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	return true
}

func parseNaturalGoalObjective(raw string) (string, *int64, bool) {
	objective := strings.TrimSpace(raw)
	if objective == "" {
		return "", nil, false
	}
	if match := goalBudgetPattern.FindStringSubmatchIndex(objective); len(match) == 6 {
		suffix := ""
		if match[4] >= 0 {
			suffix = objective[match[4]:match[5]]
		}
		budget, ok := parseNaturalGoalBudget(objective[match[2]:match[3]], suffix)
		if !ok {
			return "", nil, false
		}
		objective = strings.TrimSpace(objective[:match[0]])
		if objective == "" {
			return "", nil, false
		}
		return objective, &budget, true
	}
	if goalBudgetMarkerPattern.MatchString(objective) {
		return "", nil, false
	}
	return objective, nil, true
}

func parseNaturalGoalBudget(number, suffix string) (int64, bool) {
	value, err := strconv.ParseInt(number, 10, 64)
	if err != nil || value <= 0 {
		return 0, false
	}
	multiplier := int64(1)
	switch strings.ToLower(strings.TrimSpace(suffix)) {
	case "", "tokens", "token":
	case "k", "千":
		multiplier = 1_000
	case "万":
		multiplier = 10_000
	default:
		return 0, false
	}
	if value > math.MaxInt64/multiplier {
		return 0, false
	}
	return value * multiplier, true
}
