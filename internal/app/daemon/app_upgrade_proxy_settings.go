package daemon

import (
	"fmt"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/app/daemon/codexupgraderuntime"
	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

func (a *App) upgradeProxyMode() (string, error) {
	loaded, err := a.loadAdminConfig()
	if err != nil {
		return "", err
	}
	return config.NormalizeUpgradeProxyMode(loaded.Config.Upgrade.ProxyMode)
}

func (a *App) upgradeProxyRuntimeBusyLocked() bool {
	if a.upgradeRuntime.CheckInFlight || a.upgradeRuntime.StartInFlight || a.codexUpgradeRuntime.Active != nil || a.codexSourceSwitch != nil {
		return true
	}
	flow := a.codexUpgradeRuntime.ActiveFlow
	return flow != nil && (flow.Stage == codexupgraderuntime.OwnerFlowStageChecking || flow.Stage == codexupgraderuntime.OwnerFlowStageRunning)
}

func (a *App) upgradeProxySettingsBusyLocked() (bool, error) {
	if a.upgradeProxyRuntimeBusyLocked() {
		return true, nil
	}
	state, _, err := a.loadUpgradeStateLocked(false)
	if err != nil {
		return false, err
	}
	return pendingUpgradeBusy(state.PendingUpgrade) || a.upgradeProxyRuntimeBusyLocked(), nil
}

func (a *App) upgradeProxySettingsEventsLocked(surfaceID, requested string) []eventcontract.Event {
	loaded, err := a.loadAdminConfig()
	if err != nil {
		return upgradeProxySettingsError(surfaceID, err)
	}
	mode, err := config.NormalizeUpgradeProxyMode(loaded.Config.Upgrade.ProxyMode)
	if err != nil {
		return upgradeProxySettingsError(surfaceID, err)
	}
	statusKind, statusText := "", ""
	if strings.TrimSpace(requested) != "" {
		next, parseErr := config.NormalizeUpgradeProxyMode(requested)
		if parseErr != nil {
			return upgradeProxySettingsError(surfaceID, parseErr)
		}
		busy, checkErr := a.upgradeProxySettingsBusyLocked()
		if checkErr != nil {
			return upgradeProxySettingsError(surfaceID, checkErr)
		}
		if busy {
			statusKind, statusText = "warning", "当前正在检查或执行升级，暂时不能修改升级网络。请等待结束后再设置。"
		} else {
			if err := config.WriteUpgradeProxyMode(loaded.Path, next); err != nil {
				return upgradeProxySettingsError(surfaceID, err)
			}
			mode = next
			statusKind, statusText = "success", "设置已保存，下次检查更新或下载时生效，无需重启。"
		}
	}
	return commandPageEvents(surfaceID, buildUpgradeProxyPageView(mode, statusKind, statusText))
}

func upgradeProxySettingsError(surfaceID string, err error) []eventcontract.Event {
	return []eventcontract.Event{upgradeNoticeEvent(surfaceID, "upgrade_proxy_settings_failed", fmt.Sprintf("读取或保存升级网络设置失败：%v", err))}
}

func buildUpgradeProxyPageView(mode, statusKind, statusText string) control.FeishuPageView {
	label := "使用系统代理"
	if mode == config.UpgradeProxyDirect {
		label = "不使用代理（直连）"
	}
	return control.NormalizeFeishuPageView(control.FeishuPageView{
		CommandID:   control.FeishuCommandUpgrade,
		Title:       "升级网络",
		Breadcrumbs: control.FeishuCommandBreadcrumbsForCommand(control.FeishuCommandUpgrade, "升级网络"),
		SummarySections: []control.FeishuCardTextSection{
			control.CommandCatalogTextSection("当前设置", label),
			control.CommandCatalogTextSection("适用范围", "仅用于 Remote 和 Codex 的检查更新与升级下载，不改变飞书消息、模型请求和本地后端连接。", "Windows 读取当前系统代理；其他系统使用服务启动时保存的代理环境。"),
		},
		StatusKind: statusKind, StatusText: statusText,
		Interactive: true, DisplayStyle: control.CommandCatalogDisplayCompactButtons,
		Sections: []control.CommandCatalogSection{{Title: "代理方式", Entries: []control.CommandCatalogEntry{{Buttons: []control.CommandCatalogButton{
			control.FeishuLocalPageCommandButton("使用系统代理", "/upgrade proxy system", "", mode == config.UpgradeProxySystem),
			control.FeishuLocalPageCommandButton("不使用代理", "/upgrade proxy direct", "", mode == config.UpgradeProxyDirect),
		}}}}},
		RelatedButtons: control.FeishuCommandBackToRootButtons(control.FeishuCommandUpgrade),
	})
}
