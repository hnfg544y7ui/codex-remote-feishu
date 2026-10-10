package control

var feishuRenameCommandSpec = feishuCommandSpec{
	definition: FeishuCommandDefinition{
		ID:               FeishuCommandRename,
		GroupID:          FeishuCommandGroupCurrentWork,
		Title:            "重命名会话",
		CanonicalSlash:   "/rename",
		CanonicalMenuKey: "rename",
		ArgumentKind:     FeishuCommandArgumentText,
		ArgumentFormHint: "设备录制检查",
		ArgumentFormNote: "输入当前会话的新名称；留空不会修改原名。",
		ArgumentSubmit:   "重命名",
		Description:      "仅 Codex headless 模式可用：修改当前已选择会话的正式名称，支持中文，不改变工作区或会话路由。",
		Examples:         []string{"/rename 设备录制检查", "/rename NanoPi WLAN 3 检查"},
		ShowInHelp:       true,
		ShowInMenu:       true,
	},
	textPrefixes: []feishuCommandPrefixMatch{
		{alias: "/rename", kind: ActionRenameCommand},
	},
	menuExact: []feishuCommandMatch{
		{alias: "rename", action: Action{Kind: ActionRenameCommand, Text: "/rename"}},
	},
}
