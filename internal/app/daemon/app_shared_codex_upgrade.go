package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/codexupgrade"
	"github.com/kxn/codex-remote-feishu/internal/app/daemon/codexupgraderuntime"
	"github.com/kxn/codex-remote-feishu/internal/app/sharedcodex"
)

func (a *App) sharedCodexOwner() (sharedcodex.Maintenance, error) {
	if open := a.codexUpgradeRuntime.SharedOwner; open != nil {
		return open(a.headlessRuntime.Paths.StateDir, a.headlessRuntime.CodexAppServerURL)
	}
	return sharedcodex.Open(a.headlessRuntime.Paths.StateDir, a.headlessRuntime.CodexAppServerURL)
}

func (a *App) inspectSharedCodexInstallation(ctx context.Context) (codexupgrade.Installation, error) {
	c, err := a.sharedCodexOwner()
	if err != nil {
		return codexupgrade.Installation{}, err
	}
	status, err := c.Status(ctx)
	if err != nil {
		return codexupgrade.Installation{}, err
	}
	if status.Busy || status.State != "running" {
		return codexupgrade.Installation{}, fmt.Errorf("共享后端正在维护或等待恢复，请稍后再检查")
	}
	if status.Generation == 0 {
		return codexupgrade.Installation{}, fmt.Errorf("共享后端维护宿主需要更新后才能检查升级")
	}
	return codexupgrade.Installation{SourceKind: codexupgrade.SourceSharedBackend,
		PackageVersion: status.Version, ManagedPreviousVersion: status.PreviousVersion}, nil
}

func (a *App) runSharedCodexUpgrade(ctx context.Context, tx *codexupgraderuntime.Transaction, complete func(error)) {
	c, err := a.sharedCodexOwner()
	recovered := false
	var before sharedcodex.Status
	if err == nil {
		before, err = c.Status(ctx)
		if err == nil && (before.Generation == 0 || before.State != "running" || before.Busy || before.Version != tx.CurrentVersion) {
			err = fmt.Errorf("共享后端状态已经变化，请重新检查；宿主需要提供运行代次")
		}
	}
	if err == nil {
		var result sharedcodex.Status
		result, err = c.Upgrade(ctx, sharedcodex.Upgrade{Version: tx.TargetVersion, ExpectedVersion: tx.CurrentVersion, Rollback: tx.Rollback})
		if err == nil && (result.State != "running" || result.Version != tx.TargetVersion) {
			err = fmt.Errorf("共享后端未确认目标版本运行成功")
		}
	}
	// The shared transport exits its old wrapper on native disconnect. Existing
	// surface recovery creates a new wrapper ID; never send child.restart to the
	// transaction's obsolete IDs or treat their old snapshots as fresh evidence.
	recoveryCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if c != nil {
		if status, probeErr := c.Status(recoveryCtx); probeErr == nil && status.State == "running" && !status.Busy && before.Generation > 0 && status.Generation > 0 {
			if restoreErr := a.waitSharedCodexClients(recoveryCtx, tx, status.Generation != before.Generation); restoreErr != nil {
				if err == nil {
					err = fmt.Errorf("共享后端连接恢复失败：%w", restoreErr)
				} else {
					err = fmt.Errorf("共享后端连接恢复失败：%v；维护请求失败：%w", restoreErr, err)
				}
			} else {
				recovered = true
			}
		}
	}
	a.mu.Lock()
	if recovered {
		events := a.finishStandaloneCodexUpgradeLocked(tx)
		if len(events) != 0 {
			a.queueDaemonAsyncUIEventsLocked(events)
		}
	} else {
		tx.RecoveryRequired = true
		if err == nil {
			err = fmt.Errorf("维护请求已返回，但宿主或会话尚未通过恢复验证")
		}
		err = fmt.Errorf("共享后端维护结果尚未恢复，输入继续缓存；请修复宿主后重启 Remote。维护结果：%v", err)
	}
	a.mu.Unlock()
	a.runQueuedDaemonAsyncResults()
	if complete != nil {
		complete(err)
	}
}
