package daemon

import (
	"context"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/codexmanaged"
	"github.com/kxn/codex-remote-feishu/internal/config"
)

type ensureManagedCodex func(context.Context, string, string, []string) (string, error)

func (a *App) warmManagedCodex(ctx context.Context, startup bool, ensure ensureManagedCodex) {
	a.mu.Lock()
	if a.codexSourceSwitch != nil || a.sharedCodexBackendLocked() {
		a.mu.Unlock()
		return
	}
	a.codexRuntimeWarmInFlight++
	cfg := a.headlessRuntime
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.codexRuntimeWarmInFlight--
		a.mu.Unlock()
	}()
	if cfg.CodexRuntime.Source == config.CodexRuntimeSourceExternal {
		return
	}
	if cfg.CodexRuntime.Source == config.CodexRuntimeSourceManaged {
		a.warmStandaloneManagedCodex(ctx, cfg, startup)
		return
	}
	if strings.TrimSpace(cfg.CodexRealBinary) == "" || strings.TrimSpace(cfg.Paths.StateDir) == "" {
		return
	}
	if ensure == nil {
		ensure = codexmanaged.EnsureWithEnv
	}
	updateCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	binary, err := ensure(updateCtx, cfg.CodexRealBinary, filepath.Join(cfg.Paths.StateDir, "codex-runtime"), cfg.BaseEnv)
	if err != nil {
		log.Printf("private Codex update unavailable: %v", err)
		return
	}
	if startup {
		// Startup probes must use the same installation as the headless pool.
		// Do not replace configuration that changed while the install was running.
		a.mu.Lock()
		if a.headlessRuntime.CodexRealBinary == cfg.CodexRealBinary {
			a.headlessRuntime.CodexRealBinary = binary
		}
		a.mu.Unlock()
	}
}

func (a *App) runManagedCodexUpdates(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	idleTicker := time.NewTicker(30 * time.Second)
	defer idleTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.warmManagedCodex(ctx, false, nil)
		case <-idleTicker.C:
			a.maybeRetryCodexNativeProbeIfDue(ctx)
			a.maybeActivateManagedCodexCandidate(ctx)
		}
	}
}
