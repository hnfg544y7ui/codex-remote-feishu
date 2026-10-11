package install

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/config"
	relayruntime "github.com/kxn/codex-remote-feishu/internal/runtime"
	"github.com/kxn/codex-remote-feishu/internal/xutil"
)

const (
	upgradeHelperStopDelay      = 1200 * time.Millisecond
	upgradeHelperStopGrace      = 15 * time.Second
	upgradeHelperStartupTimeout = 45 * time.Second
	upgradeHelperPollInterval   = 500 * time.Millisecond
)

var (
	upgradeHelperObserveFunc             = observeUpgrade
	upgradeHelperSleepFunc               = time.Sleep
	upgradeHelperStartDetachedDaemonFunc = relayruntime.StartDetachedDaemon
	upgradeHelperReadPIDFunc             = relayruntime.ReadPID
	upgradeHelperTerminateProcessFunc    = relayruntime.TerminateProcess
	upgradeHelperRemoveFileFunc          = os.Remove
)

func RunUpgradeHelper(args []string, _ io.Reader, stdout, _ io.Writer, _ string) error {
	flagSet := flag.NewFlagSet("upgrade-helper", flag.ContinueOnError)
	flagSet.SetOutput(stdout)
	statePath := flagSet.String("state-path", "", "path to install-state.json")
	if err := flagSet.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if strings.TrimSpace(*statePath) == "" {
		return errors.New("-state-path is required")
	}
	return RunUpgradeHelperWithStatePath(context.Background(), *statePath)
}

func RunUpgradeHelperWithStatePath(ctx context.Context, statePath string) error {
	stateValue, err := LoadState(statePath)
	if err != nil {
		return err
	}
	if stateValue.PendingUpgrade == nil {
		return errors.New("pending upgrade is missing")
	}
	if stateValue.PendingUpgrade.Phase != PendingUpgradePhasePrepared {
		return fmt.Errorf("pending upgrade phase %q is not prepared", stateValue.PendingUpgrade.Phase)
	}
	if stateValue.PendingUpgrade.CodexSourceSwitch != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
	}
	if stateValue.RollbackCandidate == nil || strings.TrimSpace(stateValue.RollbackCandidate.BinaryPath) == "" {
		return failCodexSourceHelperBeforeStop(statePath, stateValue, errors.New("rollback candidate is missing"))
	}

	cfg, err := loadUpgradeHelperConfig(stateValue)
	if err != nil {
		return failCodexSourceHelperBeforeStop(statePath, stateValue, err)
	}
	paths := RuntimePathsForState(stateValue)
	var sourceUpdateLock *relayruntime.FileLock
	if stateValue.PendingUpgrade.CodexSourceSwitch != nil {
		lockCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		sourceUpdateLock, err = acquireCodexSourceRuntimeLock(lockCtx, stateValue, true)
		cancel()
		if err != nil {
			return failCodexSourceHelperBeforeStop(statePath, stateValue, err)
		}
		defer func() {
			if sourceUpdateLock != nil {
				_ = sourceUpdateLock.Release()
			}
		}()
	}
	releaseSourceLock := func() error {
		if sourceUpdateLock == nil {
			return nil
		}
		if err := sourceUpdateLock.Release(); err != nil {
			return err
		}
		sourceUpdateLock = nil
		return nil
	}
	rollback := func(cause error) error {
		if err := releaseSourceLock(); err != nil {
			stateValue.PendingUpgrade.Phase = PendingUpgradePhaseFailed
			_ = WriteState(statePath, stateValue)
			return fmt.Errorf("release Codex runtime lock before rollback after %v: %w", cause, err)
		}
		if stateValue.PendingUpgrade.CodexSourceSwitch != nil {
			// An exhausted source-switch deadline must not prevent recovery.
			recoveryCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			return rollbackUpgradeState(recoveryCtx, statePath, stateValue, cfg, paths, cause)
		}
		return rollbackUpgradeState(ctx, statePath, stateValue, cfg, paths, cause)
	}
	if err := validateCodexSourceSwitchConfig(stateValue); err != nil {
		return failCodexSourceHelperBeforeStop(statePath, stateValue, err)
	}

	stateValue.PendingUpgrade.Phase = PendingUpgradePhaseSwitching
	if err := WriteState(statePath, stateValue); err != nil {
		return failCodexSourceHelperBeforeStop(statePath, stateValue, err)
	}

	if err := stopCurrentDaemon(ctx, stateValue, paths); err != nil {
		return rollback(fmt.Errorf("stop current service: %w", err))
	}
	if stateValue.PendingUpgrade.CodexSourceSwitch != nil {
		if err := applyCodexSourceSwitchConfig(stateValue); err != nil {
			// A failed CAS/atomic write must not restore an obsolete config snapshot.
			candidate := *stateValue.RollbackCandidate
			candidate.ConfigSnapshots = nil
			stateValue.RollbackCandidate = &candidate
			return rollback(err)
		}
		updatedCfg, configErr := loadUpgradeHelperConfig(stateValue)
		if configErr != nil {
			return rollback(configErr)
		}
		cfg = updatedCfg
	} else if err := switchUpgradeBinary(&stateValue); err != nil {
		return rollback(fmt.Errorf("switch target binary: %w", err))
	}

	stateValue.PendingUpgrade.Phase = PendingUpgradePhaseObserving
	if err := WriteState(statePath, stateValue); err != nil {
		if stateValue.PendingUpgrade.CodexSourceSwitch != nil {
			return rollback(err)
		}
		return err
	}

	if err := releaseSourceLock(); err != nil {
		return rollback(err)
	}
	if _, err := startUpgradeDaemon(ctx, cfg, stateValue, paths); err != nil {
		return rollback(fmt.Errorf("start upgraded service: %w", err))
	}
	if err := upgradeHelperObserveFunc(ctx, cfg); err != nil {
		return rollback(err)
	}

	if stateValue.PendingUpgrade.CodexSourceSwitch != nil {
		stateValue.PendingUpgrade.Phase = PendingUpgradePhaseCommitted
		return WriteState(statePath, stateValue)
	}
	stateValue.CurrentVersion = stateValue.PendingUpgrade.TargetVersion
	stateValue.CurrentSlot = xutil.FirstNonEmpty(strings.TrimSpace(stateValue.PendingUpgrade.TargetSlot), strings.TrimSpace(stateValue.PendingUpgrade.TargetVersion))
	if stateValue.PendingUpgrade.Source != UpgradeSourceLocal {
		stateValue.InstallSource = InstallSourceRelease
	}
	stateValue.LastKnownLatestVersion = stateValue.PendingUpgrade.TargetVersion
	stateValue.PendingUpgrade.Phase = PendingUpgradePhaseCommitted
	return WriteState(statePath, stateValue)
}

type upgradeHelperBootstrapState struct {
	SetupRequired bool `json:"setupRequired"`
	Gateways      []struct {
		State string `json:"state"`
	} `json:"gateways"`
}

func observeUpgrade(ctx context.Context, cfg config.LoadedAppConfig) error {
	// fallbackAdminURL names the management page, not the HTTP API origin.
	adminURL := strings.TrimSuffix(strings.TrimRight(fallbackAdminURL(cfg.Config), "/"), "/admin")
	healthURL := strings.TrimRight(adminURL, "/") + "/healthz"
	bootstrapURL := strings.TrimRight(adminURL, "/") + "/api/admin/bootstrap-state"
	runtimeStatusURL := strings.TrimRight(adminURL, "/") + "/api/admin/runtime-status"
	statusURL := strings.TrimRight(adminURL, "/") + "/v1/status"

	deadline := time.Now().Add(upgradeHelperStartupTimeout)
	var coreHealthyAtDeadline bool
	for time.Now().Before(deadline) {
		coreHealthy, gatewayReady, err := probeUpgradeHealth(ctx, healthURL, bootstrapURL, runtimeStatusURL, statusURL)
		coreHealthyAtDeadline = err == nil && coreHealthy
		if err == nil && coreHealthy {
			if gatewayReady {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(upgradeHelperPollInterval):
		}
	}
	if coreHealthyAtDeadline {
		return errors.New("feishu gateway did not recover within the upgrade startup timeout")
	}
	return errors.New("timed out waiting for upgraded service to become healthy")
}

func probeUpgradeHealth(ctx context.Context, healthURL, bootstrapURL, runtimeStatusURL, statusURL string) (bool, bool, error) {
	if err := expectHTTPStatus(ctx, healthURL, 200); err != nil {
		return false, false, err
	}
	var bootstrapState upgradeHelperBootstrapState
	if err := fetchJSON(ctx, bootstrapURL, &bootstrapState); err != nil {
		return false, false, err
	}
	if bootstrapState.SetupRequired {
		return false, false, errors.New("upgraded service unexpectedly returned to setup-required state")
	}
	if err := expectHTTPStatus(ctx, runtimeStatusURL, 200); err != nil {
		return false, false, err
	}
	if err := expectHTTPStatus(ctx, statusURL, 200); err != nil {
		return false, false, err
	}
	return true, gatewayRecovered(bootstrapState), nil
}

func gatewayRecovered(state upgradeHelperBootstrapState) bool {
	if len(state.Gateways) == 0 {
		return true
	}
	hasConnected := false
	for _, gateway := range state.Gateways {
		switch strings.TrimSpace(strings.ToLower(gateway.State)) {
		case "connected":
			hasConnected = true
		case "disabled":
		default:
			return false
		}
	}
	return hasConnected
}

func switchUpgradeBinary(stateValue *InstallState) error {
	if stateValue == nil || stateValue.PendingUpgrade == nil {
		return errors.New("pending upgrade is missing")
	}
	targetBinary := strings.TrimSpace(stateValue.PendingUpgrade.TargetBinaryPath)
	if targetBinary == "" {
		targetSlot := xutil.FirstNonEmpty(strings.TrimSpace(stateValue.PendingUpgrade.TargetSlot), strings.TrimSpace(stateValue.PendingUpgrade.TargetVersion))
		targetBinary = filepath.Join(strings.TrimSpace(stateValue.VersionsRoot), targetSlot, xutil.ExecutableName(runtime.GOOS))
	}
	if _, err := os.Stat(targetBinary); err != nil {
		return err
	}
	// Migrate version-scoped legacy live binary to canonical instance bin dir.
	if canonicalDir, needsMigration := canonicalInstallBinDirForMigration(runtime.GOOS, *stateValue); needsMigration {
		stateValue.CurrentBinaryPath = filepath.Join(canonicalDir, filepath.Base(stateValue.CurrentBinaryPath))
	}
	if err := copyFile(targetBinary, stateValue.CurrentBinaryPath); err != nil {
		return fmt.Errorf("copy upgrade binary %s -> %s: %w", targetBinary, stateValue.CurrentBinaryPath, err)
	}
	if stateValue.PendingUpgrade.Source != UpgradeSourceLocal {
		_ = updateCurrentReleaseLink(stateValue.VersionsRoot, xutil.FirstNonEmpty(strings.TrimSpace(stateValue.PendingUpgrade.TargetSlot), strings.TrimSpace(stateValue.PendingUpgrade.TargetVersion)))
	}
	return nil
}

func stopCurrentDaemon(ctx context.Context, stateValue InstallState, paths relayruntime.Paths) error {
	return stopInstallStateProcess(ctx, stateValue, paths, stopInstallStateOptions{
		StopDelay:    upgradeHelperStopDelay,
		StopGrace:    upgradeHelperStopGrace,
		PollInterval: upgradeHelperPollInterval,
	}, runtimeControlHooks{
		Sleep:            upgradeHelperSleepFunc,
		ReadPID:          upgradeHelperReadPIDFunc,
		TerminateProcess: upgradeHelperTerminateProcessFunc,
		RemoveFile:       upgradeHelperRemoveFileFunc,
	})
}

func startUpgradeDaemon(ctx context.Context, cfg config.LoadedAppConfig, stateValue InstallState, paths relayruntime.Paths) (int, error) {
	if isManagedServiceManager(stateValue) {
		driver, ok := managedServiceDriverForManager(effectiveServiceManager(stateValue))
		if !ok {
			return 0, fmt.Errorf("unsupported managed service manager %q", effectiveServiceManager(stateValue))
		}
		if driver.InstallBeforeUpgradeRun {
			updated, err := driver.Install(ctx, stateValue)
			if err != nil {
				return 0, err
			}
			stateValue = updated
		}
		return 0, driver.Start(ctx, stateValue)
	}
	env := config.FilterEnvWithoutProxy(os.Environ())
	if cfg.Config.Feishu.UseSystemProxy {
		env = append(env, config.CaptureProxyEnv()...)
	}
	env = config.SupplementDetachedPATH(env)
	return upgradeHelperStartDetachedDaemonFunc(relayruntime.LaunchOptions{
		BinaryPath: stateValue.CurrentBinaryPath,
		ConfigPath: xutil.FirstNonEmpty(strings.TrimSpace(stateValue.ConfigPath), cfg.Path),
		Env:        env,
		Paths:      paths,
	})
}

func rollbackUpgradeState(ctx context.Context, statePath string, stateValue InstallState, cfg config.LoadedAppConfig, paths relayruntime.Paths, cause error) error {
	stopErr := stopCurrentDaemon(ctx, stateValue, paths)
	if stopErr != nil {
		stateValue.PendingUpgrade.Phase = PendingUpgradePhaseFailed
		_ = WriteState(statePath, stateValue)
		return fmt.Errorf("rollback stop failed after %v: %w", cause, stopErr)
	}
	if stateValue.RollbackCandidate != nil {
		if err := restoreConfigSnapshots(stateValue.RollbackCandidate.ConfigSnapshots); err != nil {
			stateValue.PendingUpgrade.Phase = PendingUpgradePhaseFailed
			_ = WriteState(statePath, stateValue)
			return fmt.Errorf("rollback config restore failed after %v: %w", cause, err)
		}
	}
	if stateValue.PendingUpgrade.CodexSourceSwitch == nil && stateValue.RollbackCandidate != nil && strings.TrimSpace(stateValue.RollbackCandidate.BinaryPath) != "" {
		if err := copyFile(stateValue.RollbackCandidate.BinaryPath, stateValue.CurrentBinaryPath); err != nil {
			stateValue.PendingUpgrade.Phase = PendingUpgradePhaseFailed
			_ = WriteState(statePath, stateValue)
			return fmt.Errorf("rollback copy %s -> %s failed after %v: %w", stateValue.RollbackCandidate.BinaryPath, stateValue.CurrentBinaryPath, cause, err)
		}
	}
	if stateValue.PendingUpgrade.CodexSourceSwitch == nil && stateValue.RollbackCandidate != nil {
		stateValue.CurrentVersion = stateValue.RollbackCandidate.Version
		stateValue.CurrentSlot = stateValue.RollbackCandidate.Version
	}
	stateValue.PendingUpgrade.Phase = PendingUpgradePhaseRolledBack
	if err := WriteState(statePath, stateValue); err != nil {
		return err
	}
	if _, err := startUpgradeDaemon(ctx, cfg, stateValue, paths); err != nil {
		stateValue.PendingUpgrade.Phase = PendingUpgradePhaseFailed
		_ = WriteState(statePath, stateValue)
		return fmt.Errorf("restart rollback service failed after %v: %w", cause, err)
	}
	return cause
}

func loadUpgradeHelperConfig(stateValue InstallState) (config.LoadedAppConfig, error) {
	if strings.TrimSpace(stateValue.ConfigPath) != "" {
		return config.LoadAppConfigAtPath(stateValue.ConfigPath)
	}
	return config.LoadAppConfig()
}
