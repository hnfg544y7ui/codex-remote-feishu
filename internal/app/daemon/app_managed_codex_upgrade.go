package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/codexmanaged"
	"github.com/kxn/codex-remote-feishu/internal/app/codexupgrade"
	codexupgraderuntime "github.com/kxn/codex-remote-feishu/internal/app/daemon/codexupgraderuntime"
)

func (a *App) codexUpgradeRequestCheck(ctx context.Context, req codexUpgradeStartRequest) (codexUpgradeCheckResult, error) {
	if req.Automatic {
		cfg := a.managedCodexConfig()
		state, err := a.managedCodexStore().inspect(managedCodexRoot(cfg))
		if err != nil {
			return codexUpgradeCheckResult{}, err
		}
		channel := strings.TrimSpace(cfg.CodexRuntime.Channel)
		if channel == "" {
			channel = "stable"
		}
		a.mu.Lock()
		preparedVersion := a.managedCodexCandidateVersion
		a.mu.Unlock()
		if !cfg.CodexRuntime.AutoUpdate || cfg.CodexRuntime.Version != "" || state.Active == nil || state.Candidate == nil || state.Candidate.Version != req.TargetVersion || state.Candidate.Channel != channel || preparedVersion != req.TargetVersion || !codexmanaged.IsNewerVersion(req.TargetVersion, state.Active.Version) {
			return codexUpgradeCheckResult{}, fmt.Errorf("managed Codex candidate or update policy changed")
		}
		if _, failed := state.Failed[req.TargetVersion]; failed {
			return codexUpgradeCheckResult{}, fmt.Errorf("automatic activation cannot retry a rejected Codex candidate")
		}
		installation, err := a.inspectStandaloneCodexInstallation(ctx)
		if err != nil || installation.SourceKind != codexupgrade.SourceManagedRuntime {
			return codexUpgradeCheckResult{}, fmt.Errorf("automatic activation requires managed Codex: %v", err)
		}
		return codexUpgradeCheckResult{Installation: installation, CurrentVersion: installation.CurrentVersion(), LatestVersion: req.TargetVersion, HasUpdate: req.TargetVersion != installation.CurrentVersion()}, nil
	}
	if !req.Rollback {
		return a.checkStandaloneCodexUpgrade(ctx, req.SurfaceSessionID)
	}
	installation, err := a.inspectStandaloneCodexInstallation(ctx)
	if err != nil {
		return codexUpgradeCheckResult{}, err
	}
	if !installation.SupportsRollback() || installation.ManagedPreviousVersion == "" {
		return codexUpgradeCheckResult{}, fmt.Errorf("no verified previous managed Codex release is available")
	}
	if req.TargetVersion != installation.ManagedPreviousVersion {
		return codexUpgradeCheckResult{}, fmt.Errorf("previous Codex release changed; check again before rollback")
	}
	return codexUpgradeCheckResult{Installation: installation, CurrentVersion: installation.CurrentVersion(),
		PreviousVersion: installation.ManagedPreviousVersion, LatestVersion: installation.ManagedPreviousVersion,
		CanRollback: true}, nil
}

func (a *App) runManagedCodexUpgrade(ctx context.Context, tx *codexupgraderuntime.Transaction, onComplete func(error)) {
	cfg := a.managedCodexConfig()
	h := a.managedCodexStore()
	root := managedCodexRoot(cfg)
	release, runErr := a.prepareManagedCodex(ctx, cfg, tx.TargetVersion, !tx.Rollback && !tx.Automatic)
	activated := false
	if runErr == nil {
		if tx.Rollback {
			release, runErr = h.rollback(ctx, root)
		} else {
			release, runErr = h.activate(ctx, root, release.Version)
		}
		activated = runErr == nil
	}
	if runErr == nil {
		a.setManagedCodexBinary(release)
		runErr = a.restartManagedCodexChildren(ctx, tx.RestartInstanceIDs)
	}
	if runErr != nil && activated {
		// Recovery has its own deadline; the update's timeout must not disable rollback.
		recoveryCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		// An operator rollback rejects the replaced release. A failed rollback
		// must explicitly re-verify it before undoing that rejection.
		opts := managedCodexOptions(cfg)
		opts.Version, opts.RetryFailed = tx.CurrentVersion, true
		_, restoreErr := h.prepare(recoveryCtx, root, opts, cfg.BaseEnv)
		var previous codexmanaged.Release
		if restoreErr == nil {
			previous, restoreErr = h.activate(recoveryCtx, root, tx.CurrentVersion)
		}
		if restoreErr == nil {
			a.setManagedCodexBinary(previous)
			restoreErr = a.restartManagedCodexChildren(recoveryCtx, tx.RestartInstanceIDs)
		}
		cancel()
		if restoreErr != nil {
			runErr = fmt.Errorf("%w; restore previous Codex release: %v", runErr, restoreErr)
		}
		if err := a.rejectManagedCodex(cfg, tx.TargetVersion, "worker restore failed"); err != nil {
			runErr = fmt.Errorf("%w; record rejected Codex candidate: %v", runErr, err)
		}
	}
	a.mu.Lock()
	events := a.finishStandaloneCodexUpgradeLocked(tx)
	if len(events) != 0 {
		a.queueDaemonAsyncUIEventsLocked(events)
	}
	a.mu.Unlock()
	a.runQueuedDaemonAsyncResults()
	if onComplete != nil {
		onComplete(runErr)
	}
}

func (a *App) restartManagedCodexChildren(ctx context.Context, instanceIDs []string) error {
	if a.managedCodex.restart != nil {
		return a.managedCodex.restart(ctx, instanceIDs)
	}
	for _, instanceID := range instanceIDs {
		restartCtx, cancel := context.WithTimeout(ctx, childRestartOutcomeTimeout)
		err := a.restartRelayChildCodexAndWait(restartCtx, instanceID)
		cancel()
		if err != nil {
			return fmt.Errorf("restore Codex worker %s: %w", instanceID, err)
		}
	}
	return nil
}
