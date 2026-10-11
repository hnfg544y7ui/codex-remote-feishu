package daemon

import (
	"fmt"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/app/codexupgrade"
	"github.com/kxn/codex-remote-feishu/internal/app/daemon/codexupgraderuntime"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/xutil"
)

const codexUpgradeOwnerActionRollback = "rollback"

func codexUpgradeOwnerConfirmFailurePrefix(rollback bool) string {
	if rollback {
		return "确认回退失败"
	}
	return "确认升级失败"
}

func codexUpgradeOwnerSourceLabel(source codexupgrade.SourceKind) string {
	switch source {
	case codexupgrade.SourceManagedRuntime:
		return "Remote 独立托管的官方 Codex CLI"
	case codexupgrade.SourceStandaloneNPM:
		return "外部 npm 安装"
	case codexupgrade.SourceVSCodeBundle:
		return "VS Code 集成"
	case codexupgrade.SourceStandaloneOther:
		return "外部 CLI"
	default:
		return ""
	}
}

func (a *App) confirmCodexRollbackOwnerFlowLocked(command control.DaemonCommand) []eventcontract.Event {
	flow, blocked := a.requireCodexUpgradeOwnerFlowLocked(command.SurfaceSessionID, command.PickerID, a.service.SurfaceActorUserID(command.SurfaceSessionID))
	if blocked != nil {
		return blocked
	}
	if a.codexUpgradeRuntime.Active != nil || flow.Stage != codexupgraderuntime.OwnerFlowStageReady || !flow.Checked || !flow.CanRollback || flow.SourceKind != codexupgrade.SourceManagedRuntime || strings.TrimSpace(flow.PreviousVersion) == "" || flow.PreviousVersion == flow.CurrentVersion {
		return []eventcontract.Event{upgradeNoticeEvent(command.SurfaceSessionID, "codex_upgrade_owner_invalid_stage", "当前这张 Codex 升级卡片不能回退，请先重新检查。")}
	}
	flow.Rollback = true
	flow.TargetVersion = strings.TrimSpace(flow.PreviousVersion)
	a.refreshCodexUpgradeOwnerFlowLocked(flow, codexupgraderuntime.OwnerFlowStageChecking)
	go a.runCodexUpgradeOwnerConfirm(flow.FlowID, command.SurfaceSessionID)
	return []eventcontract.Event{codexUpgradeOwnerCardEvent(command.SurfaceSessionID, flow,
		"正在确认回退条件", "progress", codexUpgradeOwnerContextSections(flow),
		codexUpgradeOwnerNoticeSections("正在确认上一版本仍然可用，并校验窗口状态是否允许回退。"), nil, false)}
}

func codexUpgradeOwnerRunningText(flow *codexupgraderuntime.OwnerCardFlowRecord) (string, string) {
	if flow.Rollback {
		return "正在回退 Codex", "正在恢复上一版本，并准备重启正在运行的 Codex。"
	}
	return "正在升级 Codex", "正在安装目标版本，并准备重启正在运行的 Codex。"
}

func codexUpgradeOwnerTerminalTitle(flow *codexupgraderuntime.OwnerCardFlowRecord, success bool) string {
	operation := "升级"
	if flow.Rollback {
		operation = "回退"
	}
	if success {
		return "Codex " + operation + "完成"
	}
	return "Codex " + operation + "失败"
}

func codexUpgradeOwnerTerminalSummary(flow *codexupgraderuntime.OwnerCardFlowRecord) string {
	operation := "升级"
	if flow.Rollback {
		operation = "回退"
	}
	return fmt.Sprintf("已%s到 %s。", operation, xutil.FirstNonEmpty(strings.TrimSpace(flow.TargetVersion), strings.TrimSpace(flow.CurrentVersion), "unknown"))
}
