package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/codexmanaged"
	"github.com/kxn/codex-remote-feishu/internal/app/daemon/codexupgraderuntime"
	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestManagedCodexWorkerFailureRestoresSelectionAndResumesSurfaces(t *testing.T) {
	a, s := newManagedCodexTestApp(t)
	s.Active = &codexmanaged.Release{Version: "0.161.0", Binary: "original-cli"}
	a.managedCodex.prepare = func(_ context.Context, _ string, opts codexmanaged.Options, _ []string) (codexmanaged.Release, error) {
		return codexmanaged.Release{Version: opts.Version, Binary: opts.Version}, nil
	}
	restarts := 0
	a.managedCodex.restart = func(ctx context.Context, ids []string) error {
		restarts++
		if ctx.Err() != nil || len(ids) != 1 || ids[0] != "worker" {
			t.Fatalf("restore scope/context: %v %v", ctx.Err(), ids)
		}
		if restarts == 1 {
			return errors.New("worker restoration failed")
		}
		return nil
	}
	a.service.MaterializeSurface("surface", "", "chat", "owner")
	a.service.PauseSurfaceDispatch("surface")
	tx := &codexupgraderuntime.Transaction{ID: "test", CurrentVersion: "0.161.0", TargetVersion: "0.162.1", RestartInstanceIDs: []string{"worker"}, PausedSurfaceIDs: map[string]bool{"surface": true}}
	a.codexUpgradeRuntime.Active = tx
	var outcome error
	a.runManagedCodexUpgrade(context.Background(), tx, func(err error) { outcome = err })
	if outcome == nil || restarts != 2 || s.Active.Version != "0.161.0" || a.headlessRuntime.CodexRealBinary != s.Active.Binary {
		t.Fatalf("recovery: err=%v restarts=%d state=%#v", outcome, restarts, s)
	}
	if s.Failed["0.162.1"].Reason == "" || a.codexUpgradeRuntime.Active != nil || a.service.Surface("surface").DispatchMode != state.DispatchModeNormal {
		t.Fatal("failure stranded transaction or paused input")
	}
}

func TestManagedCodexLaunchFreezesPolicyAndPreservesAuthEnvironment(t *testing.T) {
	settings := config.CodexRuntimeSettings{Source: "managed", Channel: "stable", Version: "0.162.1"}
	env := managedCodexLaunchEnv([]string{config.CodexRuntimeSourceEnv + "=external", config.CodexRuntimeAutoUpdateEnv + "=true", "CODEX_HOME=existing-credentials"}, settings)
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CODEX_REMOTE_CODEX_RUNTIME_") {
			t.Setenv(key, value)
		}
	}
	resolved, err := config.ResolveCodexRuntimeSettings(config.CodexRuntimeSettings{Source: "legacy", Channel: "alpha", AutoUpdate: true})
	if err != nil || resolved != settings {
		t.Fatalf("wrapper drifted from daemon policy: %#v %v", resolved, err)
	}
	found := false
	for _, entry := range env {
		found = found || entry == "CODEX_HOME=existing-credentials"
	}
	if !found {
		t.Fatal("runtime policy changed credential home")
	}
}

func TestManagedCodexRollbackCheckDoesNotRequireRegistry(t *testing.T) {
	a, s := newManagedCodexTestApp(t)
	s.Active = &codexmanaged.Release{Version: "0.162.1"}
	s.Previous = &codexmanaged.Release{Version: "0.161.0"}
	a.managedCodex.latest = func(context.Context, codexmanaged.Options, []string) (string, error) {
		t.Fatal("rollback accessed registry")
		return "", nil
	}
	req := codexUpgradeStartRequest{TargetVersion: "0.161.0", Rollback: true}
	if check, err := a.codexUpgradeRequestCheck(context.Background(), req); err != nil || !check.CanRollback {
		t.Fatalf("rollback check: %#v %v", check, err)
	}
	s.Previous.Version = "0.160.0"
	if _, err := a.codexUpgradeRequestCheck(context.Background(), req); err == nil {
		t.Fatal("stale previous version accepted")
	}
}

func TestManagedCodexMenuInspectDoesNotRetakeAppLock(t *testing.T) {
	a, s := newManagedCodexTestApp(t)
	s.Active = &codexmanaged.Release{Version: "0.162.1", Binary: "private-cli"}
	done := make(chan bool, 1)
	go func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		done <- a.standaloneCodexUpgradeVisibleLocked()
	}()
	select {
	case visible := <-done:
		if !visible {
			t.Fatal("managed upgrade menu hidden")
		}
	case <-time.After(time.Second):
		t.Fatal("managed menu inspection deadlocked while holding App.mu")
	}
}
