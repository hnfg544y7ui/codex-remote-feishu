package daemon

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/app/codexupgrade"
	"github.com/kxn/codex-remote-feishu/internal/app/daemon/codexupgraderuntime"
	"github.com/kxn/codex-remote-feishu/internal/app/sharedcodex"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

type immediateOwnerStub struct {
	ownerStub
	preview func(context.Context, sharedcodex.Upgrade) (sharedcodex.ImmediatePlan, error)
}

func (o *immediateOwnerStub) PreviewImmediate(ctx context.Context, u sharedcodex.Upgrade) (sharedcodex.ImmediatePlan, error) {
	return o.preview(ctx, u)
}

func immediateControllerFixture(t *testing.T) (*App, *lifecycleGateway, *immediateOwnerStub, *codexupgraderuntime.OwnerCardFlowRecord) {
	t.Helper()
	gateway := newLifecycleGateway()
	a, _ := newUpgradeTestApp(t, gateway)
	a.headlessRuntime.CodexAppServerURL = "ws://127.0.0.1:34567"
	a.service.MaterializeSurface("surface-1", "", "chat-1", "user-1")
	o := &immediateOwnerStub{ownerStub: ownerStub{status: sharedcodex.Status{
		Version: "0.162.1", Generation: 1, State: "running", ImmediateUpgradeSupported: true,
	}}}
	o.preview = func(_ context.Context, u sharedcodex.Upgrade) (sharedcodex.ImmediatePlan, error) {
		return sharedcodex.ImmediatePlan{Token: strings.Repeat("a", 64), Version: u.ExpectedVersion,
			TargetVersion: u.Version, Generation: 1, ExpiresAt: time.Now().Add(time.Minute), ActiveThreads: 1}, nil
	}
	a.codexUpgradeRuntime.SharedOwner = func(string, string) (sharedcodex.Maintenance, error) { return o, nil }
	a.codexUpgradeRuntime.LatestLookup = func(context.Context) (string, error) { return "0.163.0", nil }
	flow := a.newCodexUpgradeOwnerFlowLocked("surface-1", "user-1", "")
	flow.Stage, flow.SourceKind = codexupgraderuntime.OwnerFlowStageReady, codexupgrade.SourceSharedBackend
	flow.CurrentVersion, flow.TargetVersion, flow.LatestVersion = "0.162.1", "0.163.0", "0.163.0"
	flow.Checked, flow.HasUpdate, flow.ImmediateSupported = true, true, true
	return a, gateway, o, flow
}

func immediateCommand(flowID, action string) control.DaemonCommand {
	return control.DaemonCommand{SurfaceSessionID: "surface-1", PickerID: flowID, OptionID: action}
}

func TestCodexUpgradeImmediatePreviewCancelAndStaleConfirmation(t *testing.T) {
	a, gateway, o, flow := immediateControllerFixture(t)
	oldID := flow.FlowID
	a.mu.Lock()
	events := a.previewImmediateCodexUpgradeOwnerFlowLocked(immediateCommand(oldID, codexUpgradeOwnerActionImmediate))
	previewID := flow.FlowID
	a.mu.Unlock()
	if previewID == oldID || len(events) != 1 {
		t.Fatal("preview did not rotate confirmation identity")
	}
	ready := waitForCodexUpgradeOperation(t, gateway, func(op feishu.Operation) bool { return op.CardTitle == "确认立即升级 Codex" })
	if !operationHasActionValue(ready, upgradeOwnerPayloadKind, upgradeOwnerPayloadOptionKey, codexUpgradeOwnerActionImmediateConfirm) {
		t.Fatal("missing second confirmation")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if o.request.Version != "" || a.codexUpgradeRuntime.Active != nil {
		t.Fatal("preview performed maintenance")
	}
	if flow.Stage != codexupgraderuntime.OwnerFlowStageImmediateConfirm {
		t.Fatal("preview did not reach confirmation")
	}
	a.cancelImmediateCodexUpgradeOwnerFlowLocked(immediateCommand(previewID, codexUpgradeOwnerActionImmediateCancel))
	if flow.FlowID == previewID || flow.ImmediatePlan != nil || flow.Immediate {
		t.Fatal("cancel retained authority")
	}
	stale := a.confirmImmediateCodexUpgradeOwnerFlowLocked(immediateCommand(previewID, codexUpgradeOwnerActionImmediateConfirm))
	if len(stale) != 1 || a.codexUpgradeRuntime.Active != nil || o.request.Version != "" {
		t.Fatal("stale confirmation executed")
	}
}

func TestCodexUpgradeImmediatePreviewFailureReturnsReady(t *testing.T) {
	a, gateway, o, flow := immediateControllerFixture(t)
	o.preview = func(context.Context, sharedcodex.Upgrade) (sharedcodex.ImmediatePlan, error) {
		return sharedcodex.ImmediatePlan{}, errors.New("probe unavailable")
	}
	a.mu.Lock()
	a.previewImmediateCodexUpgradeOwnerFlowLocked(immediateCommand(flow.FlowID, codexUpgradeOwnerActionImmediate))
	a.mu.Unlock()
	waitForCodexUpgradeOperation(t, gateway, func(op feishu.Operation) bool {
		return strings.Contains(op.Text, "读取立即升级影响失败") || op.CardTitle == "发现新版本，普通升级需等待空闲"
	})
	a.mu.Lock()
	defer a.mu.Unlock()
	if flow.Stage != codexupgraderuntime.OwnerFlowStageReady || flow.Immediate || flow.ImmediatePlan != nil || o.request.Version != "" {
		t.Fatal("preview failure left a gate or executed upgrade")
	}
}

func TestCodexUpgradeImmediateRejectsExpiredForeignAndDuplicateActions(t *testing.T) {
	for _, which := range []string{"expired", "foreign", "checking", "oldowner"} {
		t.Run(which, func(t *testing.T) {
			a, _, o, flow := immediateControllerFixture(t)
			flow.Stage, flow.Immediate = codexupgraderuntime.OwnerFlowStageImmediateConfirm, true
			flow.ImmediatePlan = &sharedcodex.ImmediatePlan{Token: strings.Repeat("a", 64), Version: flow.CurrentVersion, TargetVersion: flow.TargetVersion, ExpiresAt: time.Now().Add(time.Minute)}
			switch which {
			case "expired":
				flow.ImmediatePlan.ExpiresAt = time.Now().Add(-time.Second)
			case "foreign":
				flow.OwnerUserID = "another-user"
			case "checking":
				flow.Stage = codexupgraderuntime.OwnerFlowStageChecking
			case "oldowner":
				flow.ImmediateSupported = false
			}
			a.mu.Lock()
			defer a.mu.Unlock()
			events := a.confirmImmediateCodexUpgradeOwnerFlowLocked(immediateCommand(flow.FlowID, codexUpgradeOwnerActionImmediateConfirm))
			if len(events) == 0 || a.codexUpgradeRuntime.Active != nil || o.request.Version != "" {
				t.Fatal("invalid confirmation performed maintenance")
			}
		})
	}
}

func TestCodexUpgradeImmediateBusyExecutionRequiresExplicitAuthorization(t *testing.T) {
	a, o := enrolledSharedFixture(t)
	o.status.ImmediateUpgradeSupported = true
	a.service.UpsertInstance(&state.InstanceRecord{InstanceID: "busy-instance", Online: true,
		Managed: true, Source: "headless", ActiveTurnID: "active-turn"})
	req := codexUpgradeStartRequest{SurfaceSessionID: "surface-1", TargetVersion: "0.163.0"}
	if err := a.startStandaloneCodexUpgrade(context.Background(), req); err == nil {
		t.Fatal("ordinary upgrade bypassed busy work")
	}
	req.Immediate = true
	if err := a.startStandaloneCodexUpgrade(context.Background(), req); err == nil {
		t.Fatal("immediate upgrade missing token accepted")
	}
	req.ConfirmationToken = strings.Repeat("a", 64)
	req.Automatic = true
	if err := a.startStandaloneCodexUpgrade(context.Background(), req); err == nil {
		t.Fatal("automatic immediate upgrade accepted")
	}
	req.Automatic = false
	var calls atomic.Int32
	o.onUpgrade = func() { calls.Add(1) }
	done := make(chan error, 1)
	req.OnComplete = func(err error) { done <- err }
	if err := a.startStandaloneCodexUpgrade(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("immediate upgrade did not complete")
	}
	if calls.Load() != 1 || !o.request.Immediate || o.request.ConfirmationToken != req.ConfirmationToken {
		t.Fatal("confirmed immediate request not forwarded exactly once")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.codexUpgradeRuntime.Active != nil {
		t.Fatal("successful recovery retained upgrade gate")
	}
}

func TestCodexUpgradeImmediateMaintenanceHoldsInitiatorQueueAcrossDisconnect(t *testing.T) {
	a, _ := newUpgradeTestApp(t, newLifecycleGateway())
	a.headlessRuntime.CodexAppServerURL = "ws://127.0.0.1:34567"
	bind := func(id string) {
		a.service.UpsertInstance(&state.InstanceRecord{InstanceID: id, Online: true, Managed: true, Source: "headless",
			WorkspaceRoot: "/tmp/workspace", WorkspaceKey: "/tmp/workspace",
			ObservedFocusedThreadID: "thread-1",
			Threads:                 map[string]*state.ThreadRecord{"thread-1": {ThreadID: "thread-1", CWD: "/tmp/workspace", Loaded: true}}})
		for _, action := range []control.Action{
			{Kind: control.ActionAttachInstance, InstanceID: id},
			{Kind: control.ActionUseThread, ThreadID: "thread-1"},
		} {
			action.SurfaceSessionID, action.ChatID, action.ActorUserID = "surface-1", "chat-1", "user-1"
			a.service.ApplySurfaceAction(action)
		}
	}
	bind("old")
	for _, message := range []string{"active", "queued"} {
		a.service.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: "surface-1",
			ChatID: "chat-1", ActorUserID: "user-1", Text: message, MessageID: message})
	}
	surface := a.service.Surface("surface-1")
	if surface.ActiveQueueItemID == "" || len(surface.QueuedQueueItemIDs) != 1 {
		t.Fatal("fixture must contain active and queued work")
	}
	active, queued := surface.ActiveQueueItemID, surface.QueuedQueueItemIDs[0]
	tx := &codexupgraderuntime.Transaction{ID: "force", Immediate: true, InitiatorSurface: "surface-1", PausedSurfaceIDs: map[string]bool{}}
	a.codexUpgradeRuntime.Active = tx
	a.pauseCodexUpgradeSurfacesLocked(tx)
	if !tx.PausedSurfaceIDs["surface-1"] {
		t.Fatal("initiator excluded from maintenance hold")
	}
	a.service.ApplyInstanceDisconnected("old")
	bind("replacement")
	for _, e := range a.service.ResumeSurfaceDispatch("surface-1", nil) {
		if e.Command != nil && e.Command.Kind == agentproto.CommandPromptSend {
			t.Fatal("queue dispatched before maintenance recovery")
		}
	}
	if surface.ActiveQueueItemID != "" || len(surface.QueuedQueueItemIDs) != 1 || surface.QueuedQueueItemIDs[0] != queued {
		t.Fatal("recovery dropped or dispatched unsent queue")
	}
	events := a.finishStandaloneCodexUpgradeLocked(tx)
	dispatched := false
	for _, e := range events {
		if e.Command != nil && e.Command.Kind == agentproto.CommandPromptSend {
			dispatched = true
		}
	}
	if !dispatched || surface.ActiveQueueItemID != queued || surface.QueueItems[active].Status != state.QueueItemFailed {
		t.Fatal("validated recovery did not release only unsent queue")
	}
}
