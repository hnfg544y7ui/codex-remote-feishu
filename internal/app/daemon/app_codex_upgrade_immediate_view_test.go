package daemon

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/app/codexupgrade"
	codexupgraderuntime "github.com/kxn/codex-remote-feishu/internal/app/daemon/codexupgraderuntime"
	"github.com/kxn/codex-remote-feishu/internal/app/sharedcodex"
)

func immediateViewFlow() *codexupgraderuntime.OwnerCardFlowRecord {
	return &codexupgraderuntime.OwnerCardFlowRecord{
		FlowID: "codex-upgrade-owner-test", MessageID: "card-test",
		SourceKind:     codexupgrade.SourceSharedBackend,
		Stage:          codexupgraderuntime.OwnerFlowStageReady,
		CurrentVersion: "0.163.0", TargetVersion: "0.164.0",
		Checked: true, HasUpdate: true, ImmediateSupported: true,
		ImmediatePlan: &sharedcodex.ImmediatePlan{
			ActiveThreads: 3, ActiveGoals: 2, UncertainRequests: 1, PendingRequests: 4,
			ThreadNames: []string{"开发会话"},
		},
	}
}

func TestCodexUpgradeImmediateButtonRemainsAvailableWhileSharedOwnerBusy(t *testing.T) {
	flow := immediateViewFlow()
	flow.CanUpgrade = false
	buttons := codexUpgradeOwnerImmediateButtons(flow)
	if len(buttons) != 1 || buttons[0].Label != "立即升级" ||
		buttons[0].CallbackValue["option_id"] != codexUpgradeOwnerActionImmediate ||
		buttons[0].CallbackValue["picker_id"] != flow.FlowID {
		t.Fatalf("busy shared owner must still offer impact preview: %#v", buttons)
	}
}

func TestCodexUpgradeImmediateButtonRejectsUnsupportedContexts(t *testing.T) {
	tests := []struct {
		name string
		edit func(*codexupgraderuntime.OwnerCardFlowRecord)
	}{
		{"private managed", func(f *codexupgraderuntime.OwnerCardFlowRecord) { f.SourceKind = codexupgrade.SourceManagedRuntime }},
		{"older owner", func(f *codexupgraderuntime.OwnerCardFlowRecord) { f.ImmediateSupported = false }},
		{"no update", func(f *codexupgraderuntime.OwnerCardFlowRecord) { f.HasUpdate = false }},
		{"unchecked", func(f *codexupgraderuntime.OwnerCardFlowRecord) { f.Checked = false }},
		{"checking", func(f *codexupgraderuntime.OwnerCardFlowRecord) { f.Stage = codexupgraderuntime.OwnerFlowStageChecking }},
		{"running", func(f *codexupgraderuntime.OwnerCardFlowRecord) { f.Stage = codexupgraderuntime.OwnerFlowStageRunning }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flow := immediateViewFlow()
			tt.edit(flow)
			if buttons := codexUpgradeOwnerImmediateButtons(flow); len(buttons) != 0 {
				t.Fatalf("must hide unsupported immediate upgrade: %#v", buttons)
			}
		})
	}
	if buttons := codexUpgradeOwnerImmediateButtons(nil); len(buttons) != 0 {
		t.Fatalf("nil flow emitted buttons: %#v", buttons)
	}
}

func TestCodexUpgradeImmediatePreviewIsSameOwnerReadOnlyLoadingCard(t *testing.T) {
	flow := immediateViewFlow()
	event := codexUpgradeOwnerImmediatePreviewEvent("surface-test", flow)
	view := event.PageView
	if view == nil || view.MessageID != flow.MessageID || !view.Patchable || view.Sealed ||
		view.Interactive || len(view.RelatedButtons) != 0 || view.ThemeKey != "progress" {
		t.Fatalf("preview must remain unsealed, read-only and attached to same card: %#v", view)
	}
	flow.MessageID = ""
	view = codexUpgradeOwnerImmediatePreviewEvent("surface-test", flow).PageView
	if view == nil || view.TrackingKey != flow.FlowID {
		t.Fatalf("new owner card must preserve flow tracking: %#v", view)
	}
}

func TestCodexUpgradeImmediateConfirmationNamesScopeAndSeparateActions(t *testing.T) {
	flow := immediateViewFlow()
	view := codexUpgradeOwnerImmediateConfirmEvent("surface-test", flow).PageView
	if view == nil || view.Title != "确认立即升级 Codex" || !view.Interactive || view.Sealed ||
		view.MessageID != flow.MessageID || len(view.RelatedButtons) != 2 {
		t.Fatalf("confirmation must stay on same interactive owner card: %#v", view)
	}
	confirm, cancel := view.RelatedButtons[0], view.RelatedButtons[1]
	if confirm.Label != "确认中断并升级" || confirm.CallbackValue["option_id"] != codexUpgradeOwnerActionImmediateConfirm ||
		confirm.CallbackValue["option_id"] == upgradeOwnerActionConfirm ||
		cancel.Label != "取消" || cancel.CallbackValue["option_id"] != codexUpgradeOwnerActionImmediateCancel {
		t.Fatalf("dangerous confirmation must not reuse normal confirm: %#v", view.RelatedButtons)
	}
	for _, button := range view.RelatedButtons {
		if button.CallbackValue["kind"] != upgradeOwnerPayloadKind || button.CallbackValue["picker_id"] != flow.FlowID {
			t.Fatalf("confirmation must retain canonical owner identity: %#v", button)
		}
	}
	text := fmt.Sprint(view.BodySections, view.NoticeSections)
	for _, required := range []string{"0.163.0", "0.164.0", "所有 ChatGPT App 和飞书会话", "正在执行的会话 [3]", "活动 Goal [2]", "结果未知的请求 [1]", "待处理请求 [4]", "暂停活动 Goal", "已中断任务不会自动继续", "结果未知的请求不会重发", "连接恢复验证通过后按原队列规则执行", "外部操作无法撤销"} {
		if !strings.Contains(text, required) {
			t.Errorf("confirmation missing %q in %q", required, text)
		}
	}
}

func TestCodexUpgradeImmediateConfirmationKeepsHostileNamesInPlainCarrier(t *testing.T) {
	flow := immediateViewFlow()
	hostile := "<at id=all>所有人</at> **名称** [跳转](https://example.invalid) `cmd`"
	flow.ImmediatePlan.ThreadNames = []string{hostile}
	event := codexUpgradeOwnerImmediateConfirmEvent("surface-test", flow)
	ops := feishu.NewProjector().ProjectEvent("chat-test", event)
	if len(ops) != 1 || ops[0].CardBody != "" {
		t.Fatalf("expected structured page projection: %#v", ops)
	}
	plainFound := false
	var visit func(any)
	visit = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			content, _ := v["content"].(string)
			if strings.Contains(content, hostile) {
				if v["tag"] != "plain_text" {
					t.Errorf("dynamic name leaked into non-plain carrier: %#v", v)
				} else {
					plainFound = true
				}
			}
			for _, child := range v {
				visit(child)
			}
		case []any:
			for _, child := range v {
				visit(child)
			}
		case []map[string]any:
			for _, child := range v {
				visit(child)
			}
		}
	}
	visit(ops[0].CardElements)
	if !plainFound {
		t.Fatalf("expected hostile name retained verbatim in plain_text: %#v", ops[0].CardElements)
	}
}

func TestCodexUpgradeImmediateNamesBoundedAndMissingPlanCannotConfirm(t *testing.T) {
	flow := immediateViewFlow()
	flow.ImmediatePlan.ThreadNames = append([]string{strings.Repeat("名", 500)}, make([]string, 30)...)
	for i := 1; i < len(flow.ImmediatePlan.ThreadNames); i++ {
		flow.ImmediatePlan.ThreadNames[i] = fmt.Sprint(i)
	}
	names := codexUpgradeImmediateThreadNames(flow.ImmediatePlan.ThreadNames)
	if len(names) != codexImmediatePreviewNames+1 || len([]rune(names[0])) != codexImmediateNameRunes+1 {
		t.Fatalf("preview must bound names and individual lengths: %#v", names)
	}
	flow.ImmediatePlan = nil
	view := codexUpgradeOwnerImmediateConfirmEvent("surface-test", flow).PageView
	if len(view.RelatedButtons) != 1 || view.RelatedButtons[0].CallbackValue["option_id"] != codexUpgradeOwnerActionImmediateCancel {
		t.Fatalf("missing preview must offer cancellation only: %#v", view)
	}
}
