package install

import (
	"path/filepath"
	"runtime"
	"testing"

	relayruntime "github.com/kxn/codex-remote-feishu/internal/runtime"
	"github.com/kxn/codex-remote-feishu/internal/xutil"
)

func TestLocalUpgradeDoesNotOverwriteStartedHelperJournal(t *testing.T) {
	baseDir := t.TempDir()
	statePath := defaultInstallStatePath(baseDir)
	current := seedBinary(t, filepath.Join(baseDir, "installed", xutil.ExecutableName(runtime.GOOS)), "old")
	source := seedBinary(t, filepath.Join(baseDir, "source", xutil.ExecutableName(runtime.GOOS)), "new")
	stateValue := InstallState{
		BaseDir: baseDir, StatePath: statePath, CurrentBinaryPath: current,
		CurrentVersion: "dev-old", CurrentTrack: ReleaseTrackAlpha,
		VersionsRoot: filepath.Join(baseDir, "releases"),
	}
	if err := WriteState(statePath, stateValue); err != nil {
		t.Fatal(err)
	}
	stubWindowsUpgradeTaskFromDetached(t)
	original := upgradeHelperStartDetachedCommandFunc
	t.Cleanup(func() { upgradeHelperStartDetachedCommandFunc = original })
	upgradeHelperStartDetachedCommandFunc = func(relayruntime.DetachedCommandOptions) (int, error) {
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
	owned, err := LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if owned.PendingUpgrade.Phase != PendingUpgradePhaseObserving || owned.CurrentVersion != "helper-owned-progress" {
		t.Fatalf("controller overwrote helper journal: phase=%s version=%s", owned.PendingUpgrade.Phase, owned.CurrentVersion)
	}
}
