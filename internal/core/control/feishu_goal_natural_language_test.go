package control

import (
	"reflect"
	"testing"
)

func TestParseFeishuNaturalGoalActionRecognizesExplicitControlSentences(t *testing.T) {
	tests := []struct {
		name string
		text string
		want GoalIntent
	}{
		{name: "create Chinese with wan budget", text: "创建Goal：完成登录重构，预算5万tokens", want: GoalIntent{Operation: "new", Objective: "完成登录重构", TokenBudget: int64Pointer(50_000)}},
		{name: "create Chinese target word", text: "新建目标为完成登录重构", want: GoalIntent{Operation: "new", Objective: "完成登录重构"}},
		{name: "create English with k budget", text: "Create a goal: finish login refactor, budget 50k tokens", want: GoalIntent{Operation: "new", Objective: "finish login refactor", TokenBudget: int64Pointer(50_000)}},
		{name: "edit Chinese", text: "把当前Goal改为：修复所有测试", want: GoalIntent{Operation: "edit", Objective: "修复所有测试"}},
		{name: "edit keeps slash-like objective text", text: "修改当前Goal：记录 --budget 不是控制参数", want: GoalIntent{Operation: "edit", Objective: "记录 --budget 不是控制参数"}},
		{name: "edit English", text: "Change the current goal to fix every test", want: GoalIntent{Operation: "edit", Objective: "fix every test"}},
		{name: "get Chinese", text: "查看当前Goal状态", want: GoalIntent{Operation: "get"}},
		{name: "get English question", text: "Check the current goal status?", want: GoalIntent{Operation: "get"}},
		{name: "pause Chinese", text: "暂停当前Goal", want: GoalIntent{Operation: "pause"}},
		{name: "pause English", text: "Pause the current goal.", want: GoalIntent{Operation: "pause"}},
		{name: "resume Chinese", text: "恢复Goal", want: GoalIntent{Operation: "resume"}},
		{name: "continue execution target", text: "继续执行目标", want: GoalIntent{Operation: "resume"}},
		{name: "continue current Goal", text: "请继续执行当前 Goal！", want: GoalIntent{Operation: "resume"}},
		{name: "resume target execution", text: "恢复当前目标的执行", want: GoalIntent{Operation: "resume"}},
		{name: "continue target", text: "继续目标", want: GoalIntent{Operation: "resume"}},
		{name: "continue English", text: "Continue the current goal.", want: GoalIntent{Operation: "resume"}},
		{name: "resume English", text: "Resume goal", want: GoalIntent{Operation: "resume"}},
		{name: "clear Chinese", text: "清除当前Goal", want: GoalIntent{Operation: "clear"}},
		{name: "clear English", text: "Clear the current goal", want: GoalIntent{Operation: "clear"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, ok := ParseFeishuNaturalGoalAction(tt.text)
			if !ok {
				t.Fatalf("ParseFeishuNaturalGoalAction(%q) did not match", tt.text)
			}
			if action.Kind != ActionGoalCommand || action.CommandID != FeishuCommandGoal {
				t.Fatalf("action routing = (%q, %q), want (%q, %q)", action.Kind, action.CommandID, ActionGoalCommand, FeishuCommandGoal)
			}
			if action.Text != tt.text {
				t.Fatalf("action.Text = %q, want original %q", action.Text, tt.text)
			}
			if action.NaturalGoal == nil || !reflect.DeepEqual(*action.NaturalGoal, tt.want) {
				t.Fatalf("action.NaturalGoal = %#v, want %#v", action.NaturalGoal, tt.want)
			}
		})
	}
}

func TestParseFeishuNaturalGoalActionRejectsAmbiguousOrUnsafeText(t *testing.T) {
	tests := []string{
		"创建目标文件 config.json",
		"请帮我完成登录重构",
		"今天的目标是完成登录重构",
		"Goal 是什么？",
		"能不能暂停当前Goal？",
		"不要暂停当前Goal",
		"不要继续执行目标",
		"如果需要就继续执行目标",
		"“继续执行目标”",
		"继续执行目标吗？",
		"继续执行目标文件的检查",
		"继续执行目标，然后删除文件",
		"继续执行",
		"如果需要就清除当前Goal",
		"假设创建Goal：完成登录重构",
		"“创建Goal：完成登录重构”",
		"> 创建Goal：完成登录重构",
		"`创建Goal：完成登录重构`",
		"```\n创建Goal：完成登录重构\n```",
		"在另一个会话创建Goal：完成登录重构",
		"为会话A创建Goal：完成登录重构",
		"创建Goal：完成登录重构？",
		"创建Goal：完成登录重构，预算很多",
		"创建Goal：完成登录重构，预算0 tokens",
		"创建Goal：完成登录重构，预算-1 tokens",
		"创建Goal：完成登录重构，预算1.5万tokens",
		"创建Goal：完成登录重构，预算999999999999999999999999万tokens",
		"/goal new 完成登录重构",
	}

	for _, text := range tests {
		t.Run(text, func(t *testing.T) {
			if action, ok := ParseFeishuNaturalGoalAction(text); ok {
				t.Fatalf("ParseFeishuNaturalGoalAction(%q) unexpectedly matched %#v", text, action)
			}
		})
	}
}

func TestParseFeishuNaturalGoalActionPreservesWhitespaceInDisplayedText(t *testing.T) {
	text := "  创建Goal：完成登录重构  "
	action, ok := ParseFeishuNaturalGoalAction(text)
	if !ok {
		t.Fatal("expected explicit Goal sentence to match")
	}
	if action.Text != text {
		t.Fatalf("action.Text = %q, want exact original %q", action.Text, text)
	}
	if action.NaturalGoal == nil || action.NaturalGoal.Objective != "完成登录重构" || action.NaturalGoal.TokenBudget != nil {
		t.Fatalf("action.NaturalGoal = %#v", action.NaturalGoal)
	}
}

func int64Pointer(value int64) *int64 {
	return &value
}
