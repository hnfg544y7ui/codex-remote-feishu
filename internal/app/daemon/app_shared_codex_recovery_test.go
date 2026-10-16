package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/daemon/codexupgraderuntime"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func sharedRecoveryFixture(t *testing.T) (*App, *ownerStub, *codexupgraderuntime.Transaction) {
	t.Helper()
	a, o := enrolledSharedFixture(t)
	a.service.UpsertInstance(&state.InstanceRecord{
		InstanceID: "old-wrapper", WorkspaceKey: "shared-workspace", Managed: true, Source: "headless", Online: true,
		CodexConnectionContract: &state.CodexConnectionContract{ConnectionContractID: "shared-contract"},
		Threads:                 map[string]*state.ThreadRecord{"thread-1": {ThreadID: "thread-1", Loaded: true}},
	})
	a.service.MaterializeSurface("surface-1", "", "chat-1", "user-1")
	surface := a.service.Surface("surface-1")
	surface.AttachedInstanceID, surface.SelectedThreadID, surface.ClaimedWorkspaceKey = "old-wrapper", "thread-1", "shared-workspace"
	tx := &codexupgraderuntime.Transaction{
		ID: "shared-recovery", CurrentVersion: "0.162.1", TargetVersion: "0.163.0",
		RestartInstanceIDs: []string{"old-wrapper", "old-pool"},
		PausedSurfaceIDs:   map[string]bool{"surface-1": true}, SharedTargets: a.sharedCodexRecoveryTargetsLocked(),
	}
	a.service.PauseSurfaceDispatch("surface-1")
	a.codexUpgradeRuntime.Active = tx
	a.sendAgentCommand = func(string, agentproto.Command) error {
		t.Fatal("shared recovery must not send child.restart")
		return nil
	}
	return a, o, tx
}

func replaceSharedClient(a *App, loaded bool) {
	a.service.Instance("old-wrapper").Online = false
	a.service.UpsertInstance(&state.InstanceRecord{
		InstanceID: "new-wrapper", WorkspaceKey: "shared-workspace", Managed: true, Source: "headless", Online: true,
		CodexConnectionContract: &state.CodexConnectionContract{ConnectionContractID: "shared-contract"},
		Threads:                 map[string]*state.ThreadRecord{"thread-1": {ThreadID: "thread-1", Loaded: loaded}},
	})
	a.service.Surface("surface-1").AttachedInstanceID = "new-wrapper"
}

func TestSharedCodexUpgradeReplacedWrapperClearsTransactionAndDispatchGate(t *testing.T) {
	a, o, tx := sharedRecoveryFixture(t)
	o.onUpgrade = func() { replaceSharedClient(a, true) }
	var result error
	a.runSharedCodexUpgrade(context.Background(), tx, func(err error) { result = err })
	if result != nil || a.codexUpgradeRuntime.Active != nil || a.service.Surface("surface-1").DispatchMode != state.DispatchModeNormal {
		t.Fatalf("replaced wrapper left upgrade gate active: %v", result)
	}
	if a.service.Surface("surface-1").SelectedThreadID != "thread-1" {
		t.Fatal("recovery changed the selected thread")
	}
}

func TestSharedCodexRecoveryRequiresNewLoadedBindingAfterGenerationChange(t *testing.T) {
	a, _, tx := sharedRecoveryFixture(t)
	if ready, err := a.sharedCodexClientsReadyLocked(tx, true); err != nil || ready {
		t.Fatalf("old online snapshot accepted after native replacement: %v %v", ready, err)
	}
	replaceSharedClient(a, false)
	if ready, err := a.sharedCodexClientsReadyLocked(tx, true); err != nil || ready {
		t.Fatalf("unloaded replacement accepted: %v %v", ready, err)
	}
	a.service.Instance("new-wrapper").Threads["thread-1"].Loaded = true
	if ready, err := a.sharedCodexClientsReadyLocked(tx, true); err != nil || !ready {
		t.Fatalf("new loaded binding not accepted: %v %v", ready, err)
	}
	a.service.Instance("new-wrapper").WorkspaceKey = "wrong-workspace"
	if _, err := a.sharedCodexClientsReadyLocked(tx, true); err == nil {
		t.Fatal("wrong workspace accepted")
	}
}

func TestSharedCodexPreparationFailureKeepsExistingClientUsable(t *testing.T) {
	a, o, tx := sharedRecoveryFixture(t)
	o.err = errors.New("download failed before native stop")
	var result error
	a.runSharedCodexUpgrade(context.Background(), tx, func(err error) { result = err })
	if result == nil || a.codexUpgradeRuntime.Active != nil || a.service.Surface("surface-1").DispatchMode != state.DispatchModeNormal || a.service.Surface("surface-1").AttachedInstanceID != "old-wrapper" {
		t.Fatal("preparation failure blocked or restarted an unchanged client")
	}
}

func TestSharedCodexRecoveryTimeoutDoesNotReleaseGate(t *testing.T) {
	a, _, tx := sharedRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := a.waitSharedCodexClients(ctx, tx, true); err == nil {
		t.Fatal("missing replacement was accepted")
	}
	if a.codexUpgradeRuntime.Active != tx || a.service.Surface("surface-1").DispatchMode == state.DispatchModeNormal {
		t.Fatal("timeout released input gate")
	}
}

func TestSharedCodexRecoveryWaitsForClearedRouteAndRejectsRetarget(t *testing.T) {
	a, _, tx := sharedRecoveryFixture(t)
	s := a.service.Surface("surface-1")
	s.SelectedThreadID = "another-thread"
	if _, err := a.sharedCodexClientsReadyLocked(tx, true); err == nil {
		t.Fatal("retarget accepted")
	}
	s.SelectedThreadID, s.AttachedInstanceID, s.ClaimedWorkspaceKey = "", "", ""
	if ready, err := a.sharedCodexClientsReadyLocked(tx, true); err != nil || ready {
		t.Fatalf("cleared disconnect route is not recovery: %v %v", ready, err)
	}
}

func TestSharedCodexRecoveryFailureNoticeDoesNotPromiseWaitingWillFinish(t *testing.T) {
	notice := codexUpgradeBlockedEvents("surface-1", true, true)[0].Notice
	if notice.Code != "codex_shared_backend_recovery" || !strings.Contains(notice.Text, "重启 Remote") || !strings.Contains(notice.Text, "已缓存") || strings.Contains(notice.Text, "等待完成") {
		t.Fatalf("misleading recovery notice: %+v", notice)
	}
}

func TestSharedCodexRollbackRecoveryRequiresReplacementEvenOnOldVersion(t *testing.T) {
	a, o, tx := sharedRecoveryFixture(t)
	o.err = errors.New("candidate startup failed; previous version restored")
	o.onUpgrade = func() { o.status.Generation++; replaceSharedClient(a, true) }
	var result error
	a.runSharedCodexUpgrade(context.Background(), tx, func(err error) { result = err })
	if result == nil || a.codexUpgradeRuntime.Active != nil || a.service.Surface("surface-1").DispatchMode != state.DispatchModeNormal {
		t.Fatal("restored old generation stranded clients")
	}
}

func TestSharedCodexUpgradeEntryPointCapturesBindingsAndUnblocksNextInput(t *testing.T) {
	a, o, _ := sharedRecoveryFixture(t)
	a.codexUpgradeRuntime.Active = nil
	a.service.ResumeSurfaceDispatch("surface-1", nil)
	o.onUpgrade = func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		target := a.codexUpgradeRuntime.Active.SharedTargets["surface-1"]
		if target.InstanceID != "old-wrapper" || target.ThreadID != "thread-1" {
			t.Errorf("entry point failed to freeze original binding: %+v", target)
		}
		replaceSharedClient(a, true)
	}
	done := make(chan error, 1)
	if err := a.startStandaloneCodexUpgrade(context.Background(), codexUpgradeStartRequest{
		SurfaceSessionID: "surface-init", TargetVersion: "0.163.0", OnComplete: func(err error) { done <- err },
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upgrade entry point did not finish after replacement")
	}
	if a.maybeHandleStandaloneCodexUpgradeActionLocked(context.Background(), control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: "surface-1"}) {
		t.Fatal("ordinary input still reports upgrading after successful recovery")
	}
	if a.service.Surface("surface-1").DispatchMode != state.DispatchModeNormal {
		t.Fatal("original surface remains paused")
	}
}
