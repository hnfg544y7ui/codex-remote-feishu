package control

var feishuScheduleCommandSpec = feishuCommandSpec{
	definition: FeishuCommandDefinition{
		ID: FeishuCommandSchedule, GroupID: FeishuCommandGroupCommonTools,
		Title: "聊天定时任务", CanonicalSlash: "/schedule", CanonicalMenuKey: "schedule",
		ArgumentKind: FeishuCommandArgumentNone,
		Description:  "在飞书聊天里用自然语言创建本机定时任务；查询、暂停、恢复和删除自己在当前聊天创建的任务。",
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
