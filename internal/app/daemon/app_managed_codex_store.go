package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/codexmanaged"
	"github.com/kxn/codex-remote-feishu/internal/app/codexprofile"
	"github.com/kxn/codex-remote-feishu/internal/app/codexupgrade"
	"github.com/kxn/codex-remote-feishu/internal/config"
)

type managedCodexHooks struct {
	inspect  func(string) (codexmanaged.State, error)
	resolve  func(context.Context, string, []string) (codexmanaged.Release, error)
	prepare  func(context.Context, string, codexmanaged.Options, []string) (codexmanaged.Release, error)
	activate func(context.Context, string, string) (codexmanaged.Release, error)
	rollback func(context.Context, string) (codexmanaged.Release, error)
	reject   func(context.Context, string, string, string) error
	latest   func(context.Context, codexmanaged.Options, []string) (string, error)
	restart  func(context.Context, []string) error
}

func (a *App) managedCodexStore() managedCodexHooks {
	h := a.managedCodex
	if h.inspect == nil {
		h.inspect = codexmanaged.Inspect
	}
	if h.resolve == nil {
		h.resolve = codexmanaged.Resolve
	}
	if h.prepare == nil {
		h.prepare = codexmanaged.Prepare
	}
	if h.activate == nil {
		h.activate = codexmanaged.ActivateContext
	}
	if h.rollback == nil {
		h.rollback = codexmanaged.RollbackContext
	}
	if h.reject == nil {
		h.reject = codexmanaged.RejectContext
	}
	if h.latest == nil {
		h.latest = codexmanaged.Latest
	}
	return h
}

func managedCodexRoot(cfg HeadlessRuntimeConfig) string {
	return filepath.Join(cfg.Paths.StateDir, "codex-runtime")
}

func managedCodexOptions(cfg HeadlessRuntimeConfig) codexmanaged.Options {
	return codexmanaged.Options{Channel: cfg.CodexRuntime.Channel, Version: cfg.CodexRuntime.Version}
}

func managedCodexLaunchEnv(env []string, settings config.CodexRuntimeSettings) []string {
	// Freeze executable ownership from the daemon startup policy. A wrapper must
	// not change source merely because config.json was edited during its lifetime.
	for key, value := range map[string]string{
		config.CodexRuntimeSourceEnv:     settings.Source,
		config.CodexRuntimeChannelEnv:    settings.Channel,
		config.CodexRuntimeVersionEnv:    settings.Version,
		config.CodexRuntimeAutoUpdateEnv: strconv.FormatBool(settings.AutoUpdate),
	} {
		env = config.UpsertEnvValue(env, key, value)
	}
	return env
}

func (a *App) managedCodexConfig() HeadlessRuntimeConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.headlessRuntime
}

func (a *App) prepareManagedCodex(ctx context.Context, cfg HeadlessRuntimeConfig, target string, retry bool) (codexmanaged.Release, error) {
	opts := managedCodexOptions(cfg)
	if target != "" {
		opts.Version = target
	}
	opts.RetryFailed = retry
	h := a.managedCodexStore()
	release, err := h.prepare(ctx, managedCodexRoot(cfg), opts, cfg.BaseEnv)
	if err != nil {
		return release, err
	}
	state, err := h.inspect(managedCodexRoot(cfg))
	if err != nil {
		return release, err
	}
	// An idempotent prepare must never reject the currently running release.
	if state.Active != nil && state.Active.Version == release.Version {
		return release, nil
	}
	if err := a.probeManagedCodex(ctx, cfg, release); err != nil {
		if rejectErr := a.rejectManagedCodex(cfg, release.Version, "capability preflight failed"); rejectErr != nil {
			return release, fmt.Errorf("%w; record rejected candidate: %v", err, rejectErr)
		}
		return release, err
	}
	return release, nil
}

func (a *App) rejectManagedCodex(cfg HeadlessRuntimeConfig, version, reason string) error {
	// Persist rejection even when the failed probe/update exhausted its deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.managedCodexStore().reject(ctx, managedCodexRoot(cfg), version, reason)
}

func (a *App) probeManagedCodex(ctx context.Context, cfg HeadlessRuntimeConfig, release codexmanaged.Release) error {
	probe := a.runCodexCapabilityPreflight
	if probe == nil {
		probe = codexprofile.RunCapabilityPreflight
	}
	probeCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	observation, err := probe(probeCtx, codexprofile.CapabilityPreflightOptions{
		BinaryPath: release.Binary, Env: cfg.BaseEnv, Version: a.serverIdentity.Version,
	})
	if err != nil {
		return fmt.Errorf("managed Codex capability preflight: %w", err)
	}
	if observation.CapabilitySet != codexprofile.CodexProfileCapabilitySetV1 {
		return fmt.Errorf("managed Codex candidate does not support the required protocol")
	}
	return nil
}

func (a *App) setManagedCodexBinary(release codexmanaged.Release) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.headlessRuntime.CodexRealBinary = release.Binary
	a.managedCodexCandidateVersion = ""
}

func (a *App) inspectManagedCodexInstallation(cfg HeadlessRuntimeConfig) (codexupgrade.Installation, error) {
	root := managedCodexRoot(cfg)
	state, err := a.managedCodexStore().inspect(root)
	if err != nil {
		return codexupgrade.Installation{}, err
	}
	info := codexupgrade.Installation{SourceKind: codexupgrade.SourceManagedRuntime, RuntimeRoot: root}
	if state.Active == nil {
		info.Problem = "managed Codex has no active installation"
		return info, nil
	}
	info.ConfiguredBinary, info.EffectiveBinary, info.ResolvedBinary = state.Active.Binary, state.Active.Binary, state.Active.Binary
	info.PackageVersion = state.Active.Version
	if state.Previous != nil {
		if _, rejected := state.Failed[state.Previous.Version]; !rejected {
			info.ManagedPreviousVersion = state.Previous.Version
		}
	}
	return info, nil
}

func (a *App) configureManagedCodexUpgrade() {
	cfg := a.managedCodexConfig()
	if cfg.CodexRuntime.Source != config.CodexRuntimeSourceManaged {
		return
	}
	// Menu visibility invokes Inspect while holding App.mu. Capture the immutable
	// startup policy here instead of recursively taking that lock in the hook.
	a.codexUpgradeRuntime.Inspect = func(context.Context, codexupgrade.InspectOptions) (codexupgrade.Installation, error) {
		return a.inspectManagedCodexInstallation(cfg)
	}
	a.codexUpgradeRuntime.LatestLookup = func(ctx context.Context) (string, error) {
		return a.managedCodexStore().latest(ctx, managedCodexOptions(cfg), cfg.BaseEnv)
	}
}
