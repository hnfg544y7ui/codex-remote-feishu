package install

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/config"
	relayruntime "github.com/kxn/codex-remote-feishu/internal/runtime"
	"github.com/kxn/codex-remote-feishu/internal/xutil"
)

func sourceSwitchFixture(t *testing.T) (InstallState, []byte) {
	t.Helper()
	base := t.TempDir()
	statePath := defaultInstallStatePath(base)
	configPath := defaultConfigPathForState(statePath)
	if err := config.WriteAppConfig(configPath, config.DefaultAppConfig()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(configPath)
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	value["futureCredential"] = json.RawMessage(`{"token":"keep-me","nested":[1,2]}`)
	raw, _ = json.MarshalIndent(value, "", "  ")
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	state := InstallState{
		StatePath: statePath, ConfigPath: configPath, BaseDir: base,
		CurrentBinaryPath: seedBinary(t, filepath.Join(base, "bin", xutil.ExecutableName(runtime.GOOS)), "same-binary"),
		CurrentVersion:    "v1.1.0.0", CurrentSlot: "local-existing-slot", CurrentTrack: ReleaseTrackBeta,
		InstallSource: InstallSourceRepo, LastKnownLatestVersion: "v1.2.0.0",
	}
	if err := WriteState(statePath, state); err != nil {
		t.Fatal(err)
	}
	return state, raw
}

func sourceSwitchOptions(state InstallState, raw []byte) CodexSourceSwitchOptions {
	return CodexSourceSwitchOptions{StatePath: state.StatePath, TargetSource: config.CodexRuntimeSourceManaged, ConfigDigest: codexSourceDigest(raw)}
}

func stubSourceSwitchLaunch(t *testing.T, fail bool) {
	t.Helper()
	original := upgradeHelperStartDetachedCommandFunc
	upgradeHelperStartDetachedCommandFunc = func(relayruntime.DetachedCommandOptions) (int, error) {
		if fail {
			return 0, errors.New("launch denied")
		}
		return 321, nil
	}
	t.Cleanup(func() { upgradeHelperStartDetachedCommandFunc = original })
}

func TestCodexSourceSwitchPreparationPreservesCurrentReleaseAndConfig(t *testing.T) {
	state, raw := sourceSwitchFixture(t)
	stubSourceSwitchLaunch(t, false)
	if err := RunCodexSourceSwitchWithStatePath(sourceSwitchOptions(state, raw)); err != nil {
		t.Fatal(err)
	}
	updated, err := LoadState(state.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	assertSourceSwitchRelease(t, state, updated)
	if updated.PendingUpgrade == nil || updated.PendingUpgrade.CodexSourceSwitch == nil || updated.PendingUpgrade.TargetBinaryPath != state.CurrentBinaryPath || updated.PendingUpgrade.TargetSlot != state.CurrentSlot {
		t.Fatalf("unexpected prepared transaction: %#v", updated.PendingUpgrade)
	}
	actual, _ := os.ReadFile(state.ConfigPath)
	if string(actual) != string(raw) {
		t.Fatal("source config changed before daemon stop")
	}
	if _, err := os.Stat(filepath.Join(updated.VersionsRoot, state.CurrentSlot)); !os.IsNotExist(err) {
		t.Fatalf("source switch unexpectedly imported/deleted the current release slot: %v", err)
	}
}

func TestCodexSourceSwitchRejectsInvalidTargetAndStaleConfiguration(t *testing.T) {
	for _, scenario := range []string{"legacy", "stale", "bad-digest"} {
		t.Run(scenario, func(t *testing.T) {
			state, raw := sourceSwitchFixture(t)
			opts := sourceSwitchOptions(state, raw)
			switch scenario {
			case "legacy":
				opts.TargetSource = "legacy"
			case "stale":
				opts.ConfigDigest = codexSourceDigest([]byte("other"))
			case "bad-digest":
				opts.ConfigDigest = "sha256:invalid"
			}
			if err := RunCodexSourceSwitchWithStatePath(opts); err == nil {
				t.Fatal("invalid source switch accepted")
			}
			updated, _ := LoadState(state.StatePath)
			if updated.PendingUpgrade != nil {
				t.Fatal("failed validation wrote transaction")
			}
		})
	}
}

func TestCodexSourceSwitchLaunchFailurePreservesPreviousRollback(t *testing.T) {
	state, raw := sourceSwitchFixture(t)
	state.RollbackCandidate = &RollbackCandidate{Version: "previous", BinaryPath: filepath.Join(state.BaseDir, "old-rollback")}
	if err := WriteState(state.StatePath, state); err != nil {
		t.Fatal(err)
	}
	stubSourceSwitchLaunch(t, true)
	if err := RunCodexSourceSwitchWithStatePath(sourceSwitchOptions(state, raw)); err == nil {
		t.Fatal("expected launch failure")
	}
	updated, _ := LoadState(state.StatePath)
	if updated.PendingUpgrade != nil || updated.RollbackCandidate == nil || updated.RollbackCandidate.Version != "previous" {
		t.Fatalf("previous rollback lost: %#v", updated)
	}
}

func TestCodexSourceSwitchHelperPreservesCredentialsAndRollsBack(t *testing.T) {
	for _, failHealth := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "rollback"}[failHealth], func(t *testing.T) {
			state, raw := sourceSwitchFixture(t)
			stubSourceSwitchLaunch(t, false)
			if err := RunCodexSourceSwitchWithStatePath(sourceSwitchOptions(state, raw)); err != nil {
				t.Fatal(err)
			}
			originalRunner, originalObserve, originalSleep := systemctlUserRunner, upgradeHelperObserveFunc, upgradeHelperSleepFunc
			t.Cleanup(func() {
				systemctlUserRunner, upgradeHelperObserveFunc, upgradeHelperSleepFunc = originalRunner, originalObserve, originalSleep
			})
			calls := 0
			systemctlUserRunner = func(_ context.Context, args ...string) (string, error) {
				calls++
				if args[0] == "start" {
					lock, err := acquireCodexSourceRuntimeLock(context.Background(), state, false)
					if err != nil {
						t.Fatalf("runtime lock retained while starting service: %v", err)
					}
					_ = lock.Release()
				}
				if args[0] == "show" {
					return "inactive\n0\n", nil
				}
				return "", nil
			}
			upgradeHelperSleepFunc = func(time.Duration) {}
			upgradeHelperObserveFunc = func(_ context.Context, cfg config.LoadedAppConfig) error {
				if cfg.Config.Wrapper.CodexRuntime.Source != "managed" {
					t.Fatalf("observing old source: %#v", cfg.Config.Wrapper.CodexRuntime)
				}
				if failHealth {
					return errors.New("health failure")
				}
				return nil
			}
			prepared, _ := LoadState(state.StatePath)
			prepared.ServiceManager = ServiceManagerSystemdUser
			if err := WriteState(state.StatePath, prepared); err != nil {
				t.Fatal(err)
			}
			err := RunUpgradeHelperWithStatePath(context.Background(), state.StatePath)
			if (err != nil) != failHealth {
				t.Fatalf("helper error: %v", err)
			}
			updated, _ := LoadState(state.StatePath)
			assertSourceSwitchRelease(t, state, updated)
			actual, _ := os.ReadFile(state.ConfigPath)
			if failHealth {
				if string(actual) != string(raw) || updated.PendingUpgrade.Phase != PendingUpgradePhaseRolledBack {
					t.Fatal("config rollback failed")
				}
			} else {
				var before, after map[string]json.RawMessage
				_ = json.Unmarshal(raw, &before)
				_ = json.Unmarshal(actual, &after)
				var credentialBefore, credentialAfter any
				_ = json.Unmarshal(before["futureCredential"], &credentialBefore)
				_ = json.Unmarshal(after["futureCredential"], &credentialAfter)
				if !reflect.DeepEqual(credentialBefore, credentialAfter) {
					t.Fatal("unknown credential lost")
				}
				loaded, err := config.LoadAppConfigAtPath(state.ConfigPath)
				if err != nil || loaded.Config.Wrapper.CodexRuntime.Source != "managed" || updated.PendingUpgrade.Phase != PendingUpgradePhaseCommitted {
					t.Fatalf("source did not commit: %v", err)
				}
			}
			wantCalls := 3
			if failHealth {
				wantCalls = 6
			}
			if calls != wantCalls {
				t.Fatalf("service calls=%d want=%d", calls, wantCalls)
			}
			binary, _ := os.ReadFile(state.CurrentBinaryPath)
			if string(binary) != "same-binary" {
				t.Fatal("source switch changed Remote binary")
			}
		})
	}
}

func TestCodexSourceSwitchRefusesLegacyUpdaterLock(t *testing.T) {
	state, raw := sourceSwitchFixture(t)
	lock, err := acquireCodexSourceRuntimeLock(context.Background(), state, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	err = RunCodexSourceSwitchWithStatePath(sourceSwitchOptions(state, raw))
	if !errors.Is(err, relayruntime.ErrLockHeld) {
		t.Fatalf("source switch ignored legacy update lock: %v", err)
	}
	updated, _ := LoadState(state.StatePath)
	if updated.PendingUpgrade != nil || updated.RollbackCandidate != nil {
		t.Fatal("busy updater source switch wrote state")
	}
}

func TestCodexSourceSwitchMalformedConfigFailsWithoutStoppingDaemon(t *testing.T) {
	state, raw := sourceSwitchFixture(t)
	stubSourceSwitchLaunch(t, false)
	if err := RunCodexSourceSwitchWithStatePath(sourceSwitchOptions(state, raw)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.ConfigPath, []byte(`{"broken":`), 0o600); err != nil {
		t.Fatal(err)
	}
	original := upgradeHelperReadPIDFunc
	upgradeHelperReadPIDFunc = func(string) (int, error) { t.Fatal("early config failure stopped daemon"); return 0, nil }
	t.Cleanup(func() { upgradeHelperReadPIDFunc = original })
	if err := RunUpgradeHelperWithStatePath(context.Background(), state.StatePath); err == nil {
		t.Fatal("malformed config accepted")
	}
	updated, _ := LoadState(state.StatePath)
	if updated.PendingUpgrade.Phase != PendingUpgradePhaseFailed {
		t.Fatalf("early failure stuck at %q", updated.PendingUpgrade.Phase)
	}
	assertSourceSwitchRelease(t, state, updated)
}

func TestCodexSourceSwitchHelperLaunchHasBoundedContext(t *testing.T) {
	state, raw := sourceSwitchFixture(t)
	state.ServiceManager = ServiceManagerSystemdUser
	if err := WriteState(state.StatePath, state); err != nil {
		t.Fatal(err)
	}
	originalGOOS, originalLaunch := serviceRuntimeGOOS, upgradeHelperStartSystemdUserTransientFunc
	serviceRuntimeGOOS = "linux"
	t.Cleanup(func() { serviceRuntimeGOOS, upgradeHelperStartSystemdUserTransientFunc = originalGOOS, originalLaunch })
	called := false
	upgradeHelperStartSystemdUserTransientFunc = func(ctx context.Context, _ systemdUserTransientCommandOptions) (string, error) {
		called = true
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 25*time.Second || time.Until(deadline) < 20*time.Second {
			t.Fatalf("helper launch context deadline=%v exists=%t", deadline, ok)
		}
		return "", errors.New("bounded launch failure")
	}
	if err := RunCodexSourceSwitchWithStatePath(sourceSwitchOptions(state, raw)); err == nil {
		t.Fatal("expected launch failure")
	}
	if !called {
		t.Fatal("native launch hook did not execute")
	}
	actual, _ := LoadState(state.StatePath)
	if actual.PendingUpgrade != nil {
		t.Fatal("failed launcher left pending transaction")
	}
}

func TestCodexSourceSwitchEarlyFailureOnlyUpdatesOwnPreparedTransaction(t *testing.T) {
	state, raw := sourceSwitchFixture(t)
	stubSourceSwitchLaunch(t, false)
	if err := RunCodexSourceSwitchWithStatePath(sourceSwitchOptions(state, raw)); err != nil {
		t.Fatal(err)
	}
	expected, _ := LoadState(state.StatePath)
	newer, _ := LoadState(state.StatePath)
	newer.PendingUpgrade.CodexSourceSwitch.TargetSource = "external"
	if err := WriteState(state.StatePath, newer); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("old helper failure")
	if err := failCodexSourceHelperBeforeStop(state.StatePath, expected, cause); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	actual, _ := LoadState(state.StatePath)
	if actual.PendingUpgrade.Phase != PendingUpgradePhasePrepared || actual.PendingUpgrade.CodexSourceSwitch.TargetSource != "external" {
		t.Fatal("old helper clobbered newer journal")
	}
	actual.LastKnownLatestVersion = "new-unrelated-metadata"
	if err := WriteState(state.StatePath, actual); err != nil {
		t.Fatal(err)
	}
	if err := failCodexSourceHelperBeforeStop(state.StatePath, actual, cause); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	failed, _ := LoadState(state.StatePath)
	if failed.PendingUpgrade.Phase != PendingUpgradePhaseFailed || failed.LastKnownLatestVersion != "new-unrelated-metadata" {
		t.Fatal("failure journal lost latest state metadata")
	}
}

func TestCodexSourceSwitchHelperRejectsEditBeforeStoppingDaemon(t *testing.T) {
	state, raw := sourceSwitchFixture(t)
	stubSourceSwitchLaunch(t, false)
	if err := RunCodexSourceSwitchWithStatePath(sourceSwitchOptions(state, raw)); err != nil {
		t.Fatal(err)
	}
	changed := append(append([]byte{}, raw...), '\n')
	if err := os.WriteFile(state.ConfigPath, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	original := upgradeHelperReadPIDFunc
	upgradeHelperReadPIDFunc = func(string) (int, error) { t.Fatal("daemon stop attempted despite stale config"); return 0, nil }
	t.Cleanup(func() { upgradeHelperReadPIDFunc = original })
	if err := RunUpgradeHelperWithStatePath(context.Background(), state.StatePath); err == nil {
		t.Fatal("stale helper accepted")
	}
	actual, _ := os.ReadFile(state.ConfigPath)
	if string(actual) != string(changed) {
		t.Fatal("stale helper overwrote config")
	}
}

func TestCodexSourceSwitchHelperDoesNotOverwriteEditDuringStop(t *testing.T) {
	state, raw := sourceSwitchFixture(t)
	stubSourceSwitchLaunch(t, false)
	if err := RunCodexSourceSwitchWithStatePath(sourceSwitchOptions(state, raw)); err != nil {
		t.Fatal(err)
	}
	prepared, _ := LoadState(state.StatePath)
	prepared.ServiceManager = ServiceManagerSystemdUser
	if err := WriteState(state.StatePath, prepared); err != nil {
		t.Fatal(err)
	}
	originalRunner, originalSleep := systemctlUserRunner, upgradeHelperSleepFunc
	t.Cleanup(func() { systemctlUserRunner, upgradeHelperSleepFunc = originalRunner, originalSleep })
	changed := append(append([]byte{}, raw...), '\n')
	edited := false
	systemctlUserRunner = func(_ context.Context, args ...string) (string, error) {
		if args[0] == "stop" && !edited {
			edited = true
			if err := os.WriteFile(state.ConfigPath, changed, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if args[0] == "show" {
			return "inactive\n0\n", nil
		}
		return "", nil
	}
	upgradeHelperSleepFunc = func(time.Duration) {}
	if err := RunUpgradeHelperWithStatePath(context.Background(), state.StatePath); err == nil {
		t.Fatal("helper accepted mid-stop edit")
	}
	actual, _ := os.ReadFile(state.ConfigPath)
	if string(actual) != string(changed) {
		t.Fatal("rollback clobbered concurrent config edit")
	}
	updated, _ := LoadState(state.StatePath)
	assertSourceSwitchRelease(t, state, updated)
	if updated.PendingUpgrade.Phase != PendingUpgradePhaseRolledBack {
		t.Fatal("daemon restart rollback was not recorded")
	}
}

func TestCodexSourceSwitchConfigChangesOnlySourceForBothDirections(t *testing.T) {
	for _, source := range []string{"managed", "external"} {
		t.Run(source, func(t *testing.T) {
			state, raw := sourceSwitchFixture(t)
			var root map[string]any
			if err := json.Unmarshal(raw, &root); err != nil {
				t.Fatal(err)
			}
			wrapper := root["wrapper"].(map[string]any)
			previous := "managed"
			if source == "managed" {
				previous = "external"
			}
			wrapper["codexRuntime"] = map[string]any{"source": previous, "channel": "alpha", "version": "0.162.1", "autoUpdate": true, "futureSetting": "preserve"}
			raw, _ = json.Marshal(root)
			if err := os.WriteFile(state.ConfigPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			state.PendingUpgrade = &PendingUpgrade{CodexSourceSwitch: &CodexSourceSwitch{TargetSource: source, ConfigDigest: codexSourceDigest(raw)}}
			if err := applyCodexSourceSwitchConfig(state); err != nil {
				t.Fatal(err)
			}
			wrapper["codexRuntime"].(map[string]any)["source"] = source
			actual, _ := os.ReadFile(state.ConfigPath)
			var after map[string]any
			if err := json.Unmarshal(actual, &after); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(root, after) {
				t.Fatal("source apply changed other settings or credentials")
			}
		})
	}
}

func assertSourceSwitchRelease(t *testing.T, before, after InstallState) {
	t.Helper()
	if before.CurrentVersion != after.CurrentVersion || before.CurrentSlot != after.CurrentSlot || before.CurrentTrack != after.CurrentTrack || before.InstallSource != after.InstallSource || before.LastKnownLatestVersion != after.LastKnownLatestVersion || before.CurrentBinaryPath != after.CurrentBinaryPath {
		t.Fatalf("source switch changed release metadata: before=%#v after=%#v", before, after)
	}
}
