package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/daemon/codexupgraderuntime"
)

func (a *App) sharedCodexRecoveryTargetsLocked() map[string]codexupgraderuntime.SharedClientTarget {
	targets := map[string]codexupgraderuntime.SharedClientTarget{}
	for _, surface := range a.service.Surfaces() {
		if surface == nil {
			continue
		}
		inst := a.service.Instance(surface.AttachedInstanceID)
		if inst == nil || !inst.Online || !a.codexUpgradeAffectsInstanceLocked(inst) {
			continue
		}
		target := codexupgraderuntime.SharedClientTarget{
			InstanceID: inst.InstanceID, ThreadID: surface.SelectedThreadID, WorkspaceKey: inst.WorkspaceKey,
		}
		if inst.CodexConnectionContract != nil {
			target.ContractID = inst.CodexConnectionContract.ConnectionContractID
		}
		targets[surface.SurfaceSessionID] = target
	}
	return targets
}

func (a *App) waitSharedCodexClients(ctx context.Context, tx *codexupgraderuntime.Transaction, replaced bool) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		a.mu.Lock()
		ready, err := a.sharedCodexClientsReadyLocked(tx, replaced)
		a.mu.Unlock()
		if err != nil || ready {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待原会话的新共享客户端恢复超时：%w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (a *App) sharedCodexClientsReadyLocked(tx *codexupgraderuntime.Transaction, replaced bool) (bool, error) {
	for surfaceID, target := range tx.SharedTargets {
		surface := a.service.Surface(surfaceID)
		// An explicit detach during maintenance releases this binding. Recovery
		// does not select another thread, clear queues or resurrect that binding.
		if surface == nil || (surface.SelectedThreadID == "" && surface.AttachedInstanceID == "" && surface.ClaimedWorkspaceKey == "" && surface.PendingHeadless == nil) {
			continue
		}
		if surface.SelectedThreadID != target.ThreadID {
			return false, fmt.Errorf("共享客户端恢复时会话绑定发生变化：%s", surfaceID)
		}
		inst := a.service.Instance(surface.AttachedInstanceID)
		if surface.PendingHeadless != nil || inst == nil || !inst.Online || (replaced && inst.InstanceID == target.InstanceID) {
			return false, nil
		}
		if !a.codexUpgradeAffectsInstanceLocked(inst) || strings.TrimSpace(inst.WorkspaceKey) != strings.TrimSpace(target.WorkspaceKey) {
			return false, fmt.Errorf("共享客户端恢复到了不同的工作区或后端：%s", surfaceID)
		}
		if target.ContractID != "" && (inst.CodexConnectionContract == nil || inst.CodexConnectionContract.ConnectionContractID != target.ContractID) {
			return false, fmt.Errorf("共享客户端恢复时连接配置不匹配：%s", surfaceID)
		}
		if target.ThreadID != "" {
			thread := inst.Threads[target.ThreadID]
			if thread == nil || !thread.Loaded {
				return false, nil
			}
		}
	}
	return true, nil
}
