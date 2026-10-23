package daemon

import (
	"fmt"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/app/codexupgrade"
	codexupgraderuntime "github.com/kxn/codex-remote-feishu/internal/app/daemon/codexupgraderuntime"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

const (
	codexImmediatePreviewNames = 8
	codexImmediateNameRunes    = 160
)

func codexUpgradeOwnerImmediateButtons(flow *codexupgraderuntime.OwnerCardFlowRecord) []control.CommandCatalogButton {
	if flow == nil || flow.SourceKind != codexupgrade.SourceSharedBackend ||
		flow.Stage != codexupgraderuntime.OwnerFlowStageReady || !flow.Checked ||
		!flow.HasUpdate || !flow.ImmediateSupported {
		return nil
	}
	// This entry deliberately does not require idle-only CanUpgrade. Stopping
	// busy work is separately authorized by the subsequent owner confirmation.
	return []control.CommandCatalogButton{
		codexUpgradeOwnerButton("立即升级", flow.FlowID, codexUpgradeOwnerActionImmediate, "default"),
	}
}

func codexUpgradeOwnerImmediatePreviewEvent(surfaceID string, flow *codexupgraderuntime.OwnerCardFlowRecord) eventcontract.Event {
	return codexUpgradeOwnerCardEvent(
		surfaceID, flow, "正在检查立即升级影响", "progress",
		codexUpgradeOwnerContextSections(flow),
		codexUpgradeOwnerNoticeSections("正在读取此共享宿主的任务和 Goal 状态；此步骤不会中断任务或执行升级。"),
		nil, false,
	)
}

func codexUpgradeOwnerImmediateConfirmEvent(surfaceID string, flow *codexupgraderuntime.OwnerCardFlowRecord) eventcontract.Event {
	sections := codexUpgradeOwnerContextSections(flow)
	buttons := []control.CommandCatalogButton{}
	if flow != nil && flow.ImmediatePlan != nil {
		plan := flow.ImmediatePlan
		sections = append(sections,
			control.CommandCatalogTextSection("影响范围", "此共享宿主连接的所有 ChatGPT App 和飞书会话，包含其他窗口和话题。"),
			control.CommandCatalogTextSection("正在执行的会话", fmt.Sprint(plan.ActiveThreads)),
			control.CommandCatalogTextSection("活动 Goal", fmt.Sprint(plan.ActiveGoals)),
			control.CommandCatalogTextSection("结果未知的请求", fmt.Sprint(plan.UncertainRequests)),
			control.CommandCatalogTextSection("待处理请求", fmt.Sprint(plan.PendingRequests)),
		)
		if names := codexUpgradeImmediateThreadNames(plan.ThreadNames); len(names) > 0 {
			sections = append(sections, control.CommandCatalogTextSection("相关会话（部分）", names...))
		}
		buttons = append(buttons, codexUpgradeOwnerButton("确认中断并升级", flow.FlowID, codexUpgradeOwnerActionImmediateConfirm, "primary"))
	}
	if flow != nil {
		buttons = append(buttons, codexUpgradeOwnerButton("取消", flow.FlowID, codexUpgradeOwnerActionImmediateCancel, "default"))
	}
	notices := codexUpgradeOwnerNoticeSections(
		"确认后会中断正在执行的任务、暂停活动 Goal，并重启此共享宿主升级 Codex。",
		"已中断任务不会自动继续，Goal 不会自动恢复，结果未知的请求不会重发；请检查实际执行结果。",
		"尚未发送的排队输入会保留，连接恢复验证通过后按原队列规则执行。",
		"已经执行的外部操作无法撤销；取消则不执行升级。",
	)
	if flow == nil || flow.ImmediatePlan == nil {
		notices = codexUpgradeOwnerNoticeSections("未取得有效的影响预览，不能确认立即升级。请取消后重新检查。")
	}
	return codexUpgradeOwnerCardEvent(surfaceID, flow, "确认立即升级 Codex", "approval", sections, notices, buttons, false)
}

func codexUpgradeImmediateThreadNames(names []string) []string {
	result := make([]string, 0, codexImmediatePreviewNames)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if len(result) == codexImmediatePreviewNames {
			result = append(result, "其余会话未逐项显示；上述中断范围仍包含此共享宿主的全部会话。")
			break
		}
		runes := []rune(name)
		if len(runes) > codexImmediateNameRunes {
			name = string(runes[:codexImmediateNameRunes]) + "…"
		}
		result = append(result, name)
	}
	return result
}
