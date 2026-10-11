package daemon

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/daemon/upgraderuntime"
	"github.com/kxn/codex-remote-feishu/internal/app/install"
	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

const codexSourcePickerPrefix = "codex-source-owner-"

func codexSourceLabel(source string) string {
	switch source {
	case config.CodexRuntimeSourceManaged:
		return "Remote 托管 Codex"
	case config.CodexRuntimeSourceExternal:
		return "现有 Codex（配置路径/PATH）"
	default:
		return "兼容模式（自动选择 CLI）"
	}
}

func (a *App) openCodexSourcePickerLocked(command control.DaemonCommand, stateValue install.InstallState) []eventcontract.Event {
	if a.codexSourceSwitch != nil || a.upgradeRuntime.StartInFlight || a.upgradeRuntime.CheckInFlight || a.codexUpgradeRuntime.Active != nil || pendingUpgradeBusy(stateValue.PendingUpgrade) {
		return codexSourceSwitchBlockedEvents(command.SurfaceSessionID)
	}
	messageID := ""
	if command.FromCardAction {
		messageID = command.SourceMessageID
	}
	flow := a.newUpgradeOwnerFlowLocked(command.SurfaceSessionID, a.service.SurfaceActorUserID(command.SurfaceSessionID), messageID, upgraderuntime.OwnerFlowStageConfirm)
	flow.FlowID = codexSourcePickerPrefix + strings.TrimPrefix(flow.FlowID, "upgrade-owner-")
	current := a.headlessRuntime.CodexRuntime.Source
	if current == "" {
		current = config.CodexRuntimeSourceLegacy
	}
	flow.CurrentVersion = current // This flow carries sources, not release versions.
	_, overridden := os.LookupEnv(config.CodexRuntimeSourceEnv)
	lines := []string{"选择后会先检查目标 CLI，再重启本机 Remote 服务。同一服务下的任务全部空闲时才能切换；登录与会话记录保留。"}
	if current == config.CodexRuntimeSourceLegacy {
		lines = append(lines, "当前是兼容模式，请先选择现有 Codex（配置路径/PATH），服务恢复后再切换到 Remote 托管 Codex。")
	}
	if overridden {
		lines = append(lines, "Codex 来源由服务环境变量指定。请先移除 CODEX_REMOTE_CODEX_RUNTIME_SOURCE，再通过这里切换。")
	}
	buttons := []control.CommandCatalogButton{
		upgradeOwnerButton("Remote 托管 Codex", flow.FlowID, config.CodexRuntimeSourceManaged, "primary", overridden || current == config.CodexRuntimeSourceManaged || current == config.CodexRuntimeSourceLegacy),
		upgradeOwnerButton("现有 Codex（配置路径/PATH）", flow.FlowID, config.CodexRuntimeSourceExternal, "default", overridden || current == config.CodexRuntimeSourceExternal),
		upgradeOwnerButton("取消", flow.FlowID, upgradeOwnerActionCancel, "default", false),
	}
	return []eventcontract.Event{upgradeOwnerCardEvent(command.SurfaceSessionID, flow, "选择 Codex 来源", "info", []control.FeishuCardTextSection{
		control.CommandCatalogTextSection("当前来源", codexSourceLabel(current)),
		control.CommandCatalogTextSection("当前 CLI", a.headlessRuntime.CodexRealBinary),
	}, upgradeOwnerNoticeSections(lines...), buttons, false)}
}

func (a *App) handleCodexSourcePickerLocked(command control.DaemonCommand) []eventcontract.Event {
	flow, blocked := a.requireUpgradeOwnerFlowLocked(command.SurfaceSessionID, command.PickerID, a.service.SurfaceActorUserID(command.SurfaceSessionID))
	if blocked != nil {
		for _, event := range blocked {
			if event.Notice != nil && event.Notice.Code == "upgrade_owner_expired" {
				event.Notice.Text = "这张来源卡片已失效，请重新发送 /upgrade codex-source。"
			}
		}
		return blocked
	}
	if flow.Stage != upgraderuntime.OwnerFlowStageConfirm || a.codexSourceSwitch != nil {
		return codexSourceSwitchBlockedEvents(command.SurfaceSessionID)
	}
	if command.OptionID == upgradeOwnerActionCancel {
		event := upgradeOwnerTerminalEvent(command.SurfaceSessionID, flow, "Codex 来源切换已取消", "info", nil, upgradeOwnerNoticeSections("当前来源保持不变。"))
		a.clearUpgradeOwnerFlowLocked()
		return []eventcontract.Event{event}
	}
	target := strings.TrimSpace(command.OptionID)
	if target != config.CodexRuntimeSourceManaged && target != config.CodexRuntimeSourceExternal {
		return []eventcontract.Event{upgradeNoticeEvent(command.SurfaceSessionID, "codex_source_invalid", "不支持的 Codex 来源。请重新发送 /upgrade codex-source。")}
	}
	if _, overridden := os.LookupEnv(config.CodexRuntimeSourceEnv); overridden {
		return a.finishCodexSourceSwitchFailureLocked(flow.FlowID, "服务环境变量覆盖了 Codex 来源，请先移除该覆盖。")
	}
	if target == flow.CurrentVersion {
		return a.finishCodexSourceSwitchFailureLocked(flow.FlowID, "已经在使用所选来源，无需切换。")
	}
	if target == config.CodexRuntimeSourceManaged && flow.CurrentVersion == config.CodexRuntimeSourceLegacy {
		return a.finishCodexSourceSwitchFailureLocked(flow.FlowID, "兼容模式请先切换到现有 Codex（配置路径/PATH），服务恢复后再选择托管 Codex。")
	}
	stateValue, _, err := a.loadUpgradeStateLocked(true)
	// State I/O temporarily releases App.mu. Revalidate the owner after that
	// yield so another callback cannot replace or start this flow concurrently.
	active := a.activeUpgradeOwnerFlowLocked()
	if active == nil || active.FlowID != flow.FlowID || flow.Stage != upgraderuntime.OwnerFlowStageConfirm || a.codexSourceSwitch != nil {
		return []eventcontract.Event{upgradeNoticeEvent(command.SurfaceSessionID, "codex_source_owner_expired", "这张来源卡片已失效或正在处理，请重新发送 /upgrade codex-source。")}
	}
	if err != nil {
		return a.finishCodexSourceSwitchFailureLocked(flow.FlowID, fmt.Sprintf("读取服务状态失败：%v", err))
	}
	if reason := a.codexSourceSwitchBusyReasonLocked(stateValue); reason != "" {
		return a.finishCodexSourceSwitchFailureLocked(flow.FlowID, "暂时不能切换："+reason)
	}
	digest, err := install.CodexSourceConfigDigest(stateValue.ConfigPath)
	if err != nil {
		return a.finishCodexSourceSwitchFailureLocked(flow.FlowID, fmt.Sprintf("读取配置失败：%v", err))
	}
	flow.TargetVersion = target
	a.refreshUpgradeOwnerFlowLocked(flow, upgraderuntime.OwnerFlowStageRunning)
	a.upgradeRuntime.StartInFlight = true
	run := &codexSourceSwitchRun{flowID: flow.FlowID, cfg: a.headlessRuntime, admittedAt: time.Now().UTC(), opts: install.CodexSourceSwitchOptions{
		StatePath: stateValue.StatePath, TargetSource: target, ConfigDigest: digest,
		GatewayID: a.service.SurfaceGatewayID(command.SurfaceSessionID), SurfaceSessionID: command.SurfaceSessionID,
		ChatID: a.service.SurfaceChatID(command.SurfaceSessionID), ActorUserID: flow.OwnerUserID, SourceMessageID: flow.MessageID,
	}}
	a.codexSourceSwitch = run
	go a.runCodexSourceSwitch(run)
	return []eventcontract.Event{codexSourceSwitchProgressEvent(flow, "正在检查目标 CLI。检查通过后将重启服务，失败则保留当前来源。", false)}
}

func codexSourceSwitchProgressEvent(flow *upgraderuntime.OwnerCardFlowRecord, detail string, sealed bool) eventcontract.Event {
	return upgradeOwnerCardEvent(flow.SurfaceSessionID, flow, "正在切换 Codex 来源", "progress", []control.FeishuCardTextSection{
		control.CommandCatalogTextSection("目标来源", codexSourceLabel(flow.TargetVersion)),
	}, upgradeOwnerNoticeSections(detail), nil, sealed)
}

func (a *App) finishCodexSourceSwitchFailureLocked(flowID, detail string) []eventcontract.Event {
	flow := a.activeUpgradeOwnerFlowLocked()
	if flow == nil || flow.FlowID != flowID {
		return nil
	}
	event := upgradeOwnerTerminalEvent(flow.SurfaceSessionID, flow, "Codex 来源未切换", "error", nil, upgradeOwnerNoticeSections(detail, "当前来源保留。请处理后重新发送 /upgrade codex-source。"))
	a.codexSourceSwitch = nil
	a.upgradeRuntime.StartInFlight = false
	a.clearUpgradeOwnerFlowLocked()
	return []eventcontract.Event{event}
}

func codexSourceSwitchBlockedEvents(surfaceID string) []eventcontract.Event {
	return []eventcontract.Event{upgradeNoticeEvent(surfaceID, "codex_source_switch_busy", "当前正在切换 Codex 来源或执行升级。请等待服务恢复后再操作。")}
}

func codexSourceSwitchAllowsAction(action control.Action) bool {
	switch action.Kind {
	case control.ActionStatus, control.ActionReactionCreated, control.ActionMessageRecalled:
		return true
	case control.ActionUpgradeOwnerFlow:
		return action.OwnerFlow != nil && strings.HasPrefix(action.OwnerFlow.FlowID, codexSourcePickerPrefix)
	default:
		return false
	}
}
