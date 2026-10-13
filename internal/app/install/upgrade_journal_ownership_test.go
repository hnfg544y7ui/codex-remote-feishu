package install

import (
	"path/filepath"
	"runtime"
	"testing"

	relayruntime "github.com/kxn/codex-remote-feishu/internal/runtime"
	"github.com/kxn/codex-remote-feishu/internal/xutil"
)

func TestLocalUpgradeDoesNotOverwriteStartedHelperJournal(t *testing.T) {
	// Other service fixtures may leave a managed unit in TestMain's shared home.
	// This test exercises the detached helper's journal handoff, so give its
	// service probe an independent home rather than launching a systemd helper.
	homeDir := t.TempDir()
	previousHome := serviceUserHomeDir
	serviceUserHomeDir = func() (string, error) { return homeDir, nil }
	t.Cleanup(func() { serviceUserHomeDir = previousHome })
	baseDir := t.TempDir()
	statePath := defaultInstallStatePath(baseDir)
	current := seedBinary(t, filepath.Join(baseDir, "installed", xutil.ExecutableName(runtime.GOOS)), "old")
	source := seedBinary(t, filepath.Join(baseDir, "source", xutil.ExecutableName(runtime.GOOS)), "new")
	stateValue := InstallState{
		BaseDir: baseDir, StatePath: statePath, CurrentBinaryPath: current,
		ServiceManager: ServiceManagerDetached,
		CurrentVersion: "dev-old", CurrentTrack: ReleaseTrackAlpha,
		VersionsRoot: filepath.Join(baseDir, "releases"),
	}
	if err := WriteState(statePath, stateValue); err != nil {
		t.Fatal(err)
	}
	stubWindowsUpgradeTaskFromDetached(t)
	original := upgradeHelperStartDetachedCommandFunc
	t.Cleanup(func() { upgradeHelperStartDetachedCommandFunc = original })
	starts := 0
	upgradeHelperStartDetachedCommandFunc = func(relayruntime.DetachedCommandOptions) (int, error) {
		starts++
		owned, err := LoadState(statePath)
		if err != nil {
			t.Fatal(err)
		}
		owned.PendingUpgrade.Phase = PendingUpgradePhaseObserving
		owned.CurrentVersion = "helper-owned-progress"
		if err := WriteState(statePath, owned); err != nil {
			t.Fatal(err)
		}
		return 123, nil
	}
	if _, err := RunLocalBinaryUpgradeWithStatePath(LocalBinaryUpgradeOptions{StatePath: statePath, SourceBinary: source}); err != nil {
		t.Fatal(err)
	}
	if starts != 1 {
		t.Fatalf("expected the journal-owning helper to start once, got %d", starts)
	}
	owned, err := LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if owned.PendingUpgrade.Phase != PendingUpgradePhaseObserving || owned.CurrentVersion != "helper-owned-progress" {
		t.Fatalf("controller overwrote helper journal: phase=%s version=%s", owned.PendingUpgrade.Phase, owned.CurrentVersion)
	}
}
