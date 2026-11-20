package control

var feishuScheduleCommandSpec = feishuCommandSpec{
	definition: FeishuCommandDefinition{
		ID: FeishuCommandSchedule, GroupID: FeishuCommandGroupCommonTools,
		Title: "聊天定时任务", CanonicalSlash: "/schedule", CanonicalMenuKey: "schedule",
		ArgumentKind: FeishuCommandArgumentNone,
		Description:  "管理你在当前聊天创建的定时任务。创建任务可直接用自然语言描述时间和内容。",
		Examples:     []string{"/schedule", "/schedule list", "/schedule pause task_ID", "/schedule resume task_ID", "/schedule delete task_ID"},
		ShowInHelp:   true, ShowInMenu: true,
	},
	textPrefixes: []feishuCommandPrefixMatch{{alias: "/schedule", kind: ActionScheduleCommand}},
	menuExact:    []feishuCommandMatch{{alias: "schedule", action: Action{Kind: ActionScheduleCommand, Text: "/schedule"}}},
}

func scheduleCommandRunsImmediately(text string) bool {
	fields := normalizedCommandFields(text)
	return len(fields) == 3 && fields[0] == "/schedule" &&
		(fields[1] == "pause" || fields[1] == "resume" || fields[1] == "delete")
}
