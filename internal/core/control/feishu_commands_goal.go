package control

func goalCommandSpec() feishuCommandSpec {
	return feishuCommandSpec{
		definition: FeishuCommandDefinition{
			ID:               FeishuCommandGoal,
			GroupID:          FeishuCommandGroupCurrentWork,
			Title:            "会话目标",
			CanonicalSlash:   "/goal",
			CanonicalMenuKey: "goal",
			ArgumentKind:     FeishuCommandArgumentText,
			ArgumentFormHint: "new|edit|pause|resume|clear",
			ArgumentFormNote: "查看、创建、编辑、暂停、恢复或清除当前会话的目标。",
			ArgumentSubmit:   "打开",
			Description:      "查看、设置、暂停或恢复当前会话的目标。",
			Examples:         []string{"/goal", "/goal new 完成登录流程重构", "/goal pause"},
			ShowInHelp:       true,
			ShowInMenu:       true,
		},
		textPrefixes: []feishuCommandPrefixMatch{
			{alias: "/goal", kind: ActionGoalCommand},
		},
		menuExact: []feishuCommandMatch{
			{alias: "goal", action: Action{Kind: ActionGoalCommand, Text: "/goal"}},
		},
	}
}
