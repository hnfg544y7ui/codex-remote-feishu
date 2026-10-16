package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestSharedCodexRecoveryAcceptsDisconnectPendingAndReplacementSequence(t *testing.T) {
	a, _, tx := sharedRecoveryFixture(t)
	a.service.ApplyInstanceDisconnected("old-wrapper")
	if s := a.service.Surface("surface-1"); s.SelectedThreadID != "" || s.AttachedInstanceID != "" {
		t.Fatal("fixture did not exercise real disconnected route clearing")
	}
	if ready, err := a.sharedCodexClientsReadyLocked(tx, true); err != nil || ready {
		t.Fatalf("disconnect gap misclassified: ready=%t err=%v", ready, err)
	}
	surface := a.service.Surface("surface-1")
	surface.PendingHeadless = &state.HeadlessLaunchRecord{InstanceID: "new-wrapper", ThreadID: "thread-1", WorkspaceKey: "shared-workspace"}
	if ready, err := a.sharedCodexClientsReadyLocked(tx, true); err != nil || ready {
		t.Fatalf("pending resume misclassified: ready=%t err=%v", ready, err)
	}
	replaceSharedClient(a, true)
	surface.PendingHeadless = nil
	// Selection is installed separately from the replacement instance hello.
	if ready, err := a.sharedCodexClientsReadyLocked(tx, true); err != nil || ready {
		t.Fatalf("new hello without selection accepted: ready=%t err=%v", ready, err)
	}
	surface.SelectedThreadID, surface.ClaimedWorkspaceKey = "thread-1", "shared-workspace"
	if ready, err := a.sharedCodexClientsReadyLocked(tx, true); err != nil || !ready {
		t.Fatalf("restored original binding rejected: ready=%t err=%v", ready, err)
	}
}

func TestSharedCodexUpgradeWaitsThroughRealRouteClearingThenUnblocksInput(t *testing.T) {
	a, o, tx := sharedRecoveryFixture(t)
	resume := make(chan struct{})
	o.onUpgrade = func() {
		a.mu.Lock()
		a.service.ApplyInstanceDisconnected("old-wrapper")
		a.service.Surface("surface-1").PendingHeadless = &state.HeadlessLaunchRecord{
			InstanceID: "new-wrapper", ThreadID: "thread-1", WorkspaceKey: "shared-workspace",
		}
		a.mu.Unlock()
		close(resume)
	}
	done := make(chan error, 1)
	go a.runSharedCodexUpgrade(context.Background(), tx, func(err error) { done <- err })
	<-resume
	select {
	case err := <-done:
		t.Fatalf("upgrade completed before pending resume resolved: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	a.mu.Lock()
	replaceSharedClient(a, true)
	s := a.service.Surface("surface-1")
	s.PendingHeadless = nil
	s.SelectedThreadID, s.ClaimedWorkspaceKey = "thread-1", "shared-workspace"
	a.mu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upgrade did not finish after original thread restored")
	}
	if a.maybeHandleStandaloneCodexUpgradeActionLocked(context.Background(), control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: "surface-1"}) {
		t.Fatal("restored conversation remains fenced")
	}
}

func TestSharedCodexRecoveryRejectsPendingDifferentThread(t *testing.T) {
	a, _, tx := sharedRecoveryFixture(t)
	a.service.Surface("surface-1").PendingHeadless = &state.HeadlessLaunchRecord{ThreadID: "different-thread"}
	if _, err := a.sharedCodexClientsReadyLocked(tx, true); err == nil {
		t.Fatal("pending retarget accepted")
	}
}
