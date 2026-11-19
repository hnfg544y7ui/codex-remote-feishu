package control

import (
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestSharedThreadModelPageShowsAuthoritativeValueWithoutOverrideControls(t *testing.T) {
	view := FeishuCatalogConfigView{
		CommandID: FeishuCommandModel, CatalogBackend: agentproto.BackendCodex,
		SharedThreadSettings: true, EffectiveValue: "current-model",
		OverrideValue: "stale-model", OverrideExtraValue: "high",
		FormOptions: []CommandCatalogFormFieldOption{
			{Label: "Current", Value: "current-model"}, {Label: "Stale", Value: "stale-model"},
		},
	}
	page := BuildFeishuCommandConfigPageView(view)
	text := configPageSummaryText(page)
	if !strings.Contains(text, "会话当前设置\ncurrent-model") || !strings.Contains(text, "后续轮次生效") {
		t.Fatalf("shared model copy missing authoritative scope: %q", text)
	}
	if strings.Contains(text, "飞书覆盖") || strings.Contains(text, "附带推理覆盖") {
		t.Fatalf("shared model page advertises stale overrides: %q", text)
	}
	form := modelPresetForm(view)
	if form == nil || form.Field.DefaultValue != "current-model" {
		t.Fatalf("preset must select native value, got %#v", form)
	}
	for _, section := range page.Sections {
		for _, entry := range section.Entries {
			for _, button := range entry.Buttons {
				if button.CommandText == "/model clear" {
					t.Fatal("shared model has no local override to clear")
				}
			}
		}
	}
}

func TestSharedThreadReasoningPageTreatsNativeNullAsAuto(t *testing.T) {
	view := FeishuCatalogConfigView{
		CommandID: FeishuCommandReasoning, CatalogBackend: agentproto.BackendCodex,
		SharedThreadSettings: true, OverrideValue: "high",
		FormOptions: []CommandCatalogFormFieldOption{{Label: "High", Value: "high"}, {Label: "Clear", Value: "clear"}},
	}
	page := BuildFeishuCommandConfigPageView(view)
	text := configPageSummaryText(page)
	if !strings.Contains(text, "会话当前设置\n自动") || strings.Contains(text, "飞书覆盖") {
		t.Fatalf("null effort must display auto without local override: %q", text)
	}
	options := reasoningOptionsForConfigView(view)
	if len(options) != 1 || options[0].Value != "high" || strings.Contains(options[0].Description, "临时") {
		t.Fatalf("wrong shared reasoning semantics: %#v", options)
	}
	view.SharedThreadSettings = false
	if got := reasoningOptionsForConfigView(view); got[1].Label != "Clear" || !strings.Contains(got[1].Description, "临时") {
		t.Fatalf("standalone semantics changed: %#v", got)
	}
}

func TestSharedThreadSettingsPageWithoutSnapshotStaysUnknown(t *testing.T) {
	for _, command := range []string{FeishuCommandModel, FeishuCommandReasoning} {
		page := BuildFeishuCommandConfigPageView(FeishuCatalogConfigView{
			CommandID: command, SharedThreadSettings: true, SharedThreadSettingsPending: true,
			EffectiveValue: "stale-value", OverrideValue: "stale-value",
		})
		text := configPageSummaryText(page)
		if !strings.Contains(text, "会话当前设置\n尚未读取") || strings.Contains(text, "stale-value") {
			t.Fatalf("unknown native settings were replaced by stale values: %q", text)
		}
	}
}
