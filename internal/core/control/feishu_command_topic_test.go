package control

import "testing"

func TestTopicTextCommandKeepsLabelAndNormalizesOperation(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"/topic", ""},
		{" /TOPIC  LIST ", "list"},
		{"/topic NEW NanoPi  开发任务", "new NanoPi  开发任务"},
		{"/topic close", "close"},
		{"/topic unknown", "unknown"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			action, ok := ParseFeishuTextActionWithoutCatalog(tc.input)
			if !ok || action.Kind != ActionTopicCommand || action.CommandID != FeishuCommandTopic || action.Value != tc.want {
				t.Fatalf("topic parse: action = %#v, ok = %v; want Value %q", action, ok, tc.want)
			}
		})
	}
	for _, input := range []string{"/topics new", "请发送 /topic new", "topic new"} {
		if action, ok := ParseFeishuTextActionWithoutCatalog(input); ok {
			t.Fatalf("unexpected topic command for %q: %#v", input, action)
		}
	}
}

func TestTopicMenuOnlyListsOrCloses(t *testing.T) {
	for _, tc := range []struct{ key, want string }{
		{"topic", "list"}, {"topic_list", "list"}, {"topic-close", "close"},
	} {
		action, ok := ParseFeishuMenuActionWithoutCatalog(tc.key)
		if !ok || action.Kind != ActionTopicCommand || action.Value != tc.want {
			t.Fatalf("menu %q = %#v, %v", tc.key, action, ok)
		}
	}
	if _, ok := ParseFeishuMenuActionWithoutCatalog("topic_new"); ok {
		t.Fatal("topic creation must start from a fresh main-group text message, not a menu callback")
	}
}

func TestTopicCatalogIsGroupOnlyAndTerminal(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		for _, tc := range []struct {
			scope   string
			visible bool
		}{
			{string(CatalogSurfaceScopeKindUser), false},
			{string(CatalogSurfaceScopeKindChat), true},
		} {
			_, ok := ResolveFeishuCommandDisplayFamily(FeishuCommandTopic, interactive, CatalogContext{ProductMode: "normal", SurfaceScopeKind: tc.scope})
			if ok != tc.visible {
				t.Fatalf("topic visibility scope=%s interactive=%v: %v", tc.scope, interactive, ok)
			}
		}
	}
	parent := Action{Kind: ActionTopicCommand, Text: "/topic list"}
	binding, ok := ResolveFeishuCommandBindingFromAction(parent)
	if !ok || binding.FamilyID != FeishuCommandTopic || binding.Kind != FeishuCommandBindingTerminalPage || binding.LauncherDisposition != FeishuFrontstageLauncherEnterTerminal || binding.FollowupPolicy.Empty() {
		t.Fatalf("topic terminal binding = %#v, %v", binding, ok)
	}
	if text := BuildFeishuActionText(ActionTopicCommand, "new 测试"); text != "/topic new 测试" {
		t.Fatalf("topic command builder = %q", text)
	}
}
