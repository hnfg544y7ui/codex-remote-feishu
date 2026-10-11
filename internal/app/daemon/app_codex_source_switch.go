package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/codexmanaged"
	"github.com/kxn/codex-remote-feishu/internal/app/daemon/headlessruntime"
	"github.com/kxn/codex-remote-feishu/internal/app/daemon/upgraderuntime"
	"github.com/kxn/codex-remote-feishu/internal/app/install"
	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

type codexSourceSwitchRun struct {
	flowID           string
	cfg              HeadlessRuntimeConfig
	opts             install.CodexSourceSwitchOptions
	admittedAt       time.Time
	helperStarted    bool
	nextObserve      time.Time
	recoveryNoticeAt time.Time
}

// The native helper restarts the whole service, so all of its backends must be
// idle. Never skip the initiating surface or reuse the narrower CLI upgrade scope.
func (a *App) codexSourceSwitchBusyReasonLocked(stateValue install.InstallState) string {
	if a.upgradeRuntime.CheckInFlight || a.codexUpgradeRuntime.Active != nil || pendingUpgradeBusy(stateValue.PendingUpgrade) || (a.upgradeRuntime.StartInFlight && a.codexSourceSwitch == nil) {
		return "已有升级正在进行"
	}
	if a.codexRuntimeWarmInFlight != 0 {
		return "Codex 安装或后台更新正在进行"
	}
	if len(a.cronRuntime.runs) != 0 || a.cronRuntime.syncInFlight || len(a.turnPatchRuntime.ActiveTx) != 0 {
		return "后台任务正在运行"
	}
	if len(a.service.PendingRemoteTurns()) != 0 || len(a.service.ActiveRemoteTurns()) != 0 {
		return "有正在运行或等待执行的任务"
	}
	for _, inst := range a.service.Instances() {
		if inst == nil {
			continue
		}
		if inst.ActiveTurnID != "" || a.service.InstanceHasPendingCompact(inst.InstanceID) || a.service.InstanceHasPendingSteer(inst.InstanceID) {
			return "实例正在执行任务或处理请求：" + inst.InstanceID
		}
	}
	for _, process := range a.managedHeadlessRuntime.Processes {
		if process != nil && (process.Status == headlessruntime.StatusStarting || process.Status == headlessruntime.StatusStopping || process.RefreshInFlight) {
			return "工作进程正在启动、停止或刷新"
		}
	}
	for _, surface := range a.service.Surfaces() {
		if reason := codexUpgradeSurfaceBusyReason(surface, ""); reason != "" {
			return "会话仍有待处理内容：" + reason
		}
	}
	return ""
}

func (a *App) prepareCodexSource(ctx context.Context, run *codexSourceSwitchRun) error {
	cfg := run.cfg
	if run.opts.TargetSource == config.CodexRuntimeSourceManaged {
		cfg.CodexRuntime.Source = config.CodexRuntimeSourceManaged
		h := a.managedCodexStore()
		state, err := h.inspect(managedCodexRoot(cfg))
		if err != nil {
			return err
		}
		if state.Active != nil && (cfg.CodexRuntime.Version == "" || cfg.CodexRuntime.Version == state.Active.Version) {
			release, err := h.resolve(ctx, managedCodexRoot(cfg), cfg.BaseEnv)
			if err != nil {
				return err
			}
			return a.probeManagedCodex(ctx, cfg, release)
		}
		release, err := a.prepareManagedCodex(ctx, cfg, "", false)
		if err != nil {
			return err
		}
		_, err = h.activate(ctx, managedCodexRoot(cfg), release.Version)
		return err
	}
	loaded, err := a.loadAdminConfig()
	if err != nil {
		return err
	}
	configured, _ := resolvedCodexRealBinarySetting(loaded)
	if strings.TrimSpace(configured) == "" {
		return fmt.Errorf("现有 Codex 路径未配置，请先在本机配置 codexRealBinary")
	}
	return a.probeManagedCodex(ctx, cfg, codexmanaged.Release{Binary: configured})
}

func (a *App) runCodexSourceSwitch(run *codexSourceSwitchRun) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	err := a.prepareCodexSource(ctx, run)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.shuttingDown || a.codexSourceSwitch != run {
		return
	}
	if err == nil {
		stateValue, _, loadErr := a.loadUpgradeStateLocked(true)
		err = loadErr
		if err == nil {
			if reason := a.codexSourceSwitchBusyReasonLocked(stateValue); reason != "" {
				err = fmt.Errorf("检查期间出现新任务：%s", reason)
			}
		}
	}
	flow := a.activeUpgradeOwnerFlowLocked()
	if flow == nil || flow.FlowID != run.flowID {
		a.codexSourceSwitch = nil
		a.upgradeRuntime.StartInFlight = false
		return
	}
	if err != nil {
		a.handleUIEventsLocked(context.Background(), a.finishCodexSourceSwitchFailureLocked(run.flowID, err.Error()))
		return
	}
	// Seal the current card before handing ownership to the persisted transaction.
	// Startup result delivery will acknowledge completion (including rollback).
	a.refreshUpgradeOwnerFlowLocked(flow, upgraderuntime.OwnerFlowStageRestarting)
	a.handleUIEventsLocked(context.Background(), []eventcontract.Event{codexSourceSwitchProgressEvent(flow, "目标 CLI 检查通过，正在重启服务。恢复后会发送切换结果。", true)})
	start := a.startCodexSourceSwitch
	if start == nil {
		start = install.RunCodexSourceSwitchWithStatePath
	}
	a.mu.Unlock()
	startErr := start(run.opts)
	a.mu.Lock()
	if a.shuttingDown || a.codexSourceSwitch != run {
		return
	}
	if startErr != nil {
		a.handleUIEventsLocked(context.Background(), a.finishCodexSourceSwitchFailureLocked(run.flowID, startErr.Error()))
	} else {
		run.helperStarted = true
		run.recoveryNoticeAt = time.Now().Add(4*time.Minute + 30*time.Second)
	}
}

// A failed helper can leave the original daemon alive. Read its persisted
// terminal result instead of waiting forever for a shutdown that never occurs.
func (a *App) observeCodexSourceSwitchLocked(ctx context.Context, now time.Time) {
	run := a.codexSourceSwitch
	if run == nil || !run.helperStarted || now.Before(run.nextObserve) {
		return
	}
	run.nextObserve = now.Add(time.Second)
	stateValue, _, err := a.loadUpgradeStateLocked(false)
	if err == nil && stateValue.PendingUpgrade != nil {
		pending := stateValue.PendingUpgrade
		change := pending.CodexSourceSwitch
		if change != nil && pending.RequestedAt != nil && !pending.RequestedAt.Before(run.admittedAt) && change.ConfigDigest == run.opts.ConfigDigest && change.TargetSource == run.opts.TargetSource {
			switch pending.Phase {
			case install.PendingUpgradePhaseFailed, install.PendingUpgradePhaseRolledBack:
				loaded, loadErr := a.loadAdminConfig()
				if loadErr == nil && normalizedCodexSource(loaded.Config.Wrapper.CodexRuntime.Source) == normalizedCodexSource(run.cfg.CodexRuntime.Source) {
					a.handleUIEventsLocked(ctx, a.finishCodexSourceSwitchFailureLocked(run.flowID, "切换助手已结束，当前来源保持原配置。请检查日志后重试。"))
					return
				}
			case install.PendingUpgradePhaseCommitted:
				// The running daemon still holds its frozen old startup policy.
				// Never unfreeze merely because a helper reported success.
				if !run.recoveryNoticeAt.IsZero() {
					run.recoveryNoticeAt = now
				}
			}
		}
	}
	if !run.recoveryNoticeAt.IsZero() && !now.Before(run.recoveryNoticeAt) {
		run.recoveryNoticeAt = time.Time{}
		a.handleUIEventsLocked(ctx, []eventcontract.Event{upgradeNoticeEvent(run.opts.SurfaceSessionID, "codex_source_recovery", "Codex 来源切换超时。为避免和切换助手并发操作，输入仍暂停。请在主机终端执行 codex-remote service restart，再发送 /upgrade codex-source 检查。")})
	}
}

func normalizedCodexSource(source string) string {
	source = strings.ToLower(strings.TrimSpace(source))
	if source == "" {
		return config.CodexRuntimeSourceLegacy
	}
	return source
}
