package control

import "strings"

var feishuTopicCommandSpec = feishuCommandSpec{
	definition: FeishuCommandDefinition{
		ID:               FeishuCommandTopic,
		GroupID:          FeishuCommandGroupCollaboration,
		Title:            "话题列表",
		CanonicalSlash:   "/topic",
		CanonicalMenuKey: "topic",
		ArgumentKind:     FeishuCommandArgumentNone,
		Description:      "查看本群话题。要新建话题，在群主界面发送 /topic new 名称。",
		Examples:         []string{"/topic list", "/topic new 开发任务", "/topic close"},
		ShowInHelp:       true,
		ShowInMenu:       true,
	},
	textPrefixes: []feishuCommandPrefixMatch{
		{alias: "/topic", kind: ActionTopicCommand},
	},
	menuExact: []feishuCommandMatch{
		{alias: "topic", action: Action{Kind: ActionTopicCommand, Text: "/topic list"}},
		{alias: "topiclist", action: Action{Kind: ActionTopicCommand, Text: "/topic list"}},
		{alias: "topicclose", action: Action{Kind: ActionTopicCommand, Text: "/topic close"}},
	},
}

// Keep the label's case and internal spacing intact, while normalizing the
// subcommand for the behavior layer. Unknown arguments stay visible there so
// they produce command guidance instead of falling through as AI input.
func normalizeTopicCommandArgument(text string) string {
	fields := strings.Fields(text)
	if len(fields) <= 1 {
		return ""
	}
	argument := strings.TrimSpace(text[len(fields[0]):])
	subcommand := fields[1]
	return strings.ToLower(subcommand) + argument[len(subcommand):]
}
