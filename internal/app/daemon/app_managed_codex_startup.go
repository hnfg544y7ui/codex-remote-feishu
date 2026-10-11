package daemon

import (
	"context"
	"log"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/config"
)

func (a *App) warmStandaloneManagedCodex(ctx context.Context, cfg HeadlessRuntimeConfig, startup bool) {
	if cfg.Paths.StateDir == "" {
		return
	}
	updateCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	h := a.managedCodexStore()
	root := managedCodexRoot(cfg)
	if startup {
		state, err := h.inspect(root)
		if err != nil {
			log.Printf("managed Codex startup state unavailable: %v", err)
			return
		}
		if state.Active != nil {
			release, err := h.resolve(updateCtx, root, cfg.BaseEnv)
			if err != nil {
				log.Printf("managed Codex active release unavailable; repair its installation: %v", err)
				return
			}
			a.setManagedCodexBinary(release)
			return
		}
		release, err := a.prepareManagedCodex(updateCtx, cfg, "", false)
		if err == nil {
			release, err = h.activate(updateCtx, root, release.Version)
		}
		if err != nil {
			log.Printf("managed Codex bootstrap unavailable: %v", err)
			return
		}
		a.setManagedCodexBinary(release)
		return
	}
	if !cfg.CodexRuntime.AutoUpdate || cfg.CodexRuntime.Version != "" {
		return
	}
	release, err := a.prepareManagedCodex(updateCtx, cfg, "", false)
	if err != nil {
		log.Printf("managed Codex background preparation unavailable; active release retained: %v", err)
		return
	}
	a.mu.Lock()
	a.managedCodexCandidateVersion = release.Version
	a.mu.Unlock()
	a.maybeActivateManagedCodexCandidate(ctx)
}

func (a *App) maybeActivateManagedCodexCandidate(ctx context.Context) {
	cfg := a.managedCodexConfig()
	if cfg.CodexRuntime.Source != config.CodexRuntimeSourceManaged || !cfg.CodexRuntime.AutoUpdate || cfg.CodexRuntime.Version != "" {
		return
	}
	state, err := a.managedCodexStore().inspect(managedCodexRoot(cfg))
	if err != nil || state.Candidate == nil || state.Active == nil {
		return
	}
	if state.Candidate.Version == state.Active.Version {
		return
	}
	a.mu.Lock()
	busy := a.codexUpgradeRuntime.Active != nil || a.managedCodexCandidateVersion != state.Candidate.Version || len(a.codexUpgradeBusyReasonsLocked("")) != 0
	a.mu.Unlock()
	if busy {
		return
	}
	if err := a.startStandaloneCodexUpgrade(ctx, codexUpgradeStartRequest{TargetVersion: state.Candidate.Version, Automatic: true}); err != nil {
		log.Printf("managed Codex candidate remains pending: %v", err)
	}
}
