package control

import (
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestParseRenameCommandPreservesNameText(t *testing.T) {
	for _, text := range []string{
		"/rename",
		"/rename 设备录制检查",
		" /rename  NanoPi  WLAN 3 检查  ",
		"/RENAME 中文名称",
		"/rename\t中文 与 English",
		"/rename \"中文名称\"",
	} {
		t.Run(text, func(t *testing.T) {
			action, ok := ParseFeishuTextActionWithoutCatalog(text)
			if !ok || action.Kind != ActionRenameCommand || action.CommandID != FeishuCommandRename {
				t.Fatalf("unexpected rename action: %#v, ok=%v", action, ok)
			}
			if action.Text != strings.TrimSpace(text) {
				t.Fatalf("rename text = %q, want %q", action.Text, strings.TrimSpace(text))
			}
		})
	}
	for _, text := range []string{"/renameXYZ 中文", "/renamed 中文", "rename 中文"} {
		if action, ok := ParseFeishuTextActionWithoutCatalog(text); ok {
			t.Fatalf("unexpected prefix match for %q: %#v", text, action)
		}
	}
}

func TestRenameCommandMenuAndActionRoundTrip(t *testing.T) {
	action, ok := ParseFeishuMenuActionWithoutCatalog("rename")
	if !ok || action.Kind != ActionRenameCommand || action.CommandID != FeishuCommandRename || action.Text != "/rename" {
		t.Fatalf("unexpected rename menu action: %#v, ok=%v", action, ok)
	}
	if kind, ok := ActionKindForFeishuCommandID(FeishuCommandRename); !ok || kind != ActionRenameCommand {
		t.Fatalf("unexpected rename action kind: %q, ok=%v", kind, ok)
	}
	if id, ok := FeishuCommandIDForActionKind(ActionRenameCommand); !ok || id != FeishuCommandRename {
		t.Fatalf("unexpected rename command id: %q, ok=%v", id, ok)
	}
	if got := BuildFeishuActionText(ActionRenameCommand, "NanoPi  WLAN 检查"); got != "/rename NanoPi  WLAN 检查" {
		t.Fatalf("rename action text = %q", got)
	}
	binding, ok := ResolveFeishuCommandBindingFromAction(action)
	if !ok || binding.FamilyID != FeishuCommandRename || binding.Kind != FeishuCommandBindingOwnerEntry {
		t.Fatalf("unexpected rename binding: %#v, ok=%v", binding, ok)
	}
	if _, ok := binding.IntentFromAction(action); ok {
		t.Fatal("rename must dispatch to its owner handler instead of intercepting with a config-flow intent")
	}
}

func TestRenameCommandGenericForm(t *testing.T) {
	form, ok := FeishuCommandForm(FeishuCommandRename)
	if !ok || form == nil {
		t.Fatal("expected rename argument form")
	}
	if form.CommandID != FeishuCommandRename || form.CommandText != "/rename" || form.SubmitLabel != "重命名" ||
		form.Field.Name != "command_args" || form.Field.Kind != CommandCatalogFormFieldText {
		t.Fatalf("unexpected rename form: %#v", form)
	}
	if !strings.Contains(form.Field.Label, "留空不会修改原名") {
		t.Fatalf("empty-name behavior missing from form label: %q", form.Field.Label)
	}
}

func TestRenameCommandSupportIsHeadlessCodexOnly(t *testing.T) {
	cases := []struct {
		name    string
		ctx     CatalogContext
		allowed bool
	}{
		{name: "codex", ctx: CatalogContext{}, allowed: true},
		{name: "vscode", ctx: CatalogContext{ProductMode: "vscode"}},
		{name: "claude", ctx: CatalogContext{Backend: agentproto.BackendClaude}},
		{name: "opencode", ctx: CatalogContext{Backend: agentproto.BackendOpenCode}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			support, ok := ResolveFeishuCommandSupport(tc.ctx, FeishuCommandRename)
			if !ok || support.Visible != tc.allowed || support.DispatchAllowed != tc.allowed {
				t.Fatalf("unexpected rename support: %#v, ok=%v", support, ok)
			}
			if !tc.allowed && (support.Kind != FeishuCommandSupportReject || !strings.Contains(support.Note, "/mode codex")) {
				t.Fatalf("unsupported rename needs explicit headless guidance: %#v", support)
			}
			for _, interactive := range []bool{false, true} {
				_, visible := ResolveFeishuCommandDisplayFamily(FeishuCommandRename, interactive, tc.ctx)
				if visible != tc.allowed {
					t.Fatalf("rename display visible=%v, interactive=%v, want %v", visible, interactive, tc.allowed)
				}
			}
		})
	}
}

func TestRenameCommandIsVisibleInCodexHelpAndMenu(t *testing.T) {
	for _, catalog := range []FeishuPageView{FeishuCommandHelpPageView(), FeishuCommandMenuPageView()} {
		found := false
		for _, section := range catalog.Sections {
			for _, entry := range section.Entries {
				for _, command := range entry.Commands {
					if command == "/rename" {
						found = true
					}
				}
			}
		}
		if !found {
			t.Fatalf("rename is missing from %q", catalog.Title)
		}
	}
}
