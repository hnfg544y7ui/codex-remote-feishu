package upgradeshim

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/install"
	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/shim"
)

func TestResolveStatePathUsesSidecar(t *testing.T) {
	dir := t.TempDir()
	entrypoint := filepath.Join(dir, "upgrade-helper", "codex-remote-upgrade-shim")
	statePath := filepath.Join(dir, "install-state.json")
	if err := os.MkdirAll(filepath.Dir(entrypoint), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(statePath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := shim.WriteSidecar(shim.SidecarPath(entrypoint), shim.Sidecar{
		InstallStatePath: statePath,
		InstanceID:       "stable",
	}, shim.ModeUpgrade); err != nil {
		t.Fatalf("WriteSidecar: %v", err)
	}

	got, err := resolveStatePath(entrypoint)
	if err != nil {
		t.Fatalf("resolveStatePath: %v", err)
	}
	if got != statePath {
		t.Fatalf("state path = %q, want %q", got, statePath)
	}
}

// Exercise the real install helper through the shim boundary. A malformed
// config fails before any process/service operation. Older helper builds that
// drop the source marker would leave the journal prepared instead of recording
// a source-specific failure, reproducing the stale embedded helper regression.
func TestRunMainPreservesCodexSourceMarkerThroughRealHelper(t *testing.T) {
	dir := t.TempDir()
	entrypoint := filepath.Join(dir, "upgrade-helper", "codex-remote-upgrade-shim")
	statePath := filepath.Join(dir, "install-state.json")
	configPath := filepath.Join(dir, "config.json")
	if err := os.MkdirAll(filepath.Dir(entrypoint), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entrypoint, []byte("isolated-shim"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"broken":`), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := install.CodexSourceConfigDigest(configPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	state := install.InstallState{
		ConfigPath: configPath, StatePath: statePath,
		CurrentVersion: "v1.1.0.0", CurrentSlot: "existing-slot", CurrentBinaryPath: entrypoint,
		RollbackCandidate: &install.RollbackCandidate{BinaryPath: filepath.Join(dir, "unused-backup")},
		PendingUpgrade: &install.PendingUpgrade{
			Phase: install.PendingUpgradePhasePrepared, Source: install.UpgradeSourceLocal,
			TargetVersion: "v1.1.0.0", TargetSlot: "existing-slot", TargetBinaryPath: entrypoint,
			CodexSourceSwitch: &install.CodexSourceSwitch{TargetSource: config.CodexRuntimeSourceExternal, ConfigDigest: digest}, RequestedAt: &now,
		},
	}
	if err := install.WriteState(statePath, state); err != nil {
		t.Fatal(err)
	}
	if err := shim.WriteSidecar(shim.SidecarPath(entrypoint), shim.Sidecar{InstallStatePath: statePath}, shim.ModeUpgrade); err != nil {
		t.Fatal(err)
	}
	originalExecutable, originalRun := osExecutable, runUpgradeHelperWithStatePath
	t.Cleanup(func() { osExecutable, runUpgradeHelperWithStatePath = originalExecutable, originalRun })
	osExecutable = func() (string, error) { return entrypoint, nil }
	runUpgradeHelperWithStatePath = install.RunUpgradeHelperWithStatePath
	if code := RunMain(nil); code != 1 {
		t.Fatalf("RunMain code=%d, want pre-stop config failure", code)
	}
	updated, err := install.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if updated.PendingUpgrade == nil || updated.PendingUpgrade.CodexSourceSwitch == nil || *updated.PendingUpgrade.CodexSourceSwitch != *state.PendingUpgrade.CodexSourceSwitch {
		t.Fatal("upgrade shim dropped source-only transaction marker")
	}
	if updated.PendingUpgrade.Phase != install.PendingUpgradePhaseFailed {
		t.Fatalf("source-specific failure did not reach install helper: phase=%s", updated.PendingUpgrade.Phase)
	}
	if updated.CurrentVersion != state.CurrentVersion || updated.CurrentSlot != state.CurrentSlot || updated.CurrentBinaryPath != state.CurrentBinaryPath {
		t.Fatal("pre-stop helper changed release metadata")
	}
	binary, err := os.ReadFile(entrypoint)
	if err != nil || string(binary) != "isolated-shim" {
		t.Fatal("helper unexpectedly replaced current binary")
	}
}

func TestRunMainInvokesUpgradeHelperWithResolvedState(t *testing.T) {
	dir := t.TempDir()
	entrypoint := filepath.Join(dir, "upgrade-helper", "codex-remote-upgrade-shim")
	statePath := filepath.Join(dir, "install-state.json")
	if err := os.MkdirAll(filepath.Dir(entrypoint), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(entrypoint, []byte("shim"), 0o755); err != nil {
		t.Fatalf("WriteFile entrypoint: %v", err)
	}
	if err := os.WriteFile(statePath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile state: %v", err)
	}
	if err := shim.WriteSidecar(shim.SidecarPath(entrypoint), shim.Sidecar{
		InstallStatePath: statePath,
	}, shim.ModeUpgrade); err != nil {
		t.Fatalf("WriteSidecar: %v", err)
	}

	originalExecutable := osExecutable
	originalRun := runUpgradeHelperWithStatePath
	defer func() {
		osExecutable = originalExecutable
		runUpgradeHelperWithStatePath = originalRun
	}()
	osExecutable = func() (string, error) { return entrypoint, nil }
	var gotStatePath string
	runUpgradeHelperWithStatePath = func(_ context.Context, path string) error {
		gotStatePath = path
		return nil
	}

	if code := RunMain(nil); code != 0 {
		t.Fatalf("RunMain() = %d, want 0", code)
	}
	if gotStatePath != statePath {
		t.Fatalf("state path = %q, want %q", gotStatePath, statePath)
	}
}
