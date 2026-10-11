package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/app/codexmanaged"
	"github.com/kxn/codex-remote-feishu/internal/app/codexprofile"
	"github.com/kxn/codex-remote-feishu/internal/app/install"
	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func sourcePickerFixture(t *testing.T, source string) (*App, *lifecycleGateway, string) {
	t.Helper()
	gateway := newLifecycleGateway()
	app, statePath := newUpgradeTestApp(t, gateway)
	configPath := filepath.Join(filepath.Dir(statePath), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"wrapper":{"codexRealBinary":"existing-cli","codexRuntime":{"source":"`+source+`"}},"credential":"keep"}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := install.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	s.ConfigPath = configPath
	if err := install.WriteState(statePath, s); err != nil {
		t.Fatal(err)
	}
	app.headlessRuntime.CodexRuntime.Source = source
	app.admin.loadConfig = func() (config.LoadedAppConfig, error) {
		cfg := config.DefaultAppConfig()
		cfg.Wrapper.CodexRealBinary = "existing-cli"
		return config.LoadedAppConfig{Config: cfg}, nil
	}
	app.service.MaterializeSurface("surface-1", "app-1", "chat-1", "user-1")
	app.HandleAction(context.Background(), control.Action{Kind: control.ActionUpgradeCommand, GatewayID: "app-1", SurfaceSessionID: "surface-1", ChatID: "chat-1", ActorUserID: "user-1", Text: "/upgrade codex-source"})
	return app, gateway, configPath
}

func sourcePickerClick(app *App, option string) {
	app.HandleAction(context.Background(), control.Action{Kind: control.ActionUpgradeOwnerFlow, GatewayID: "app-1", SurfaceSessionID: "surface-1", ChatID: "chat-1", ActorUserID: "user-1", OwnerFlow: &control.ActionOwnerCardFlow{FlowID: app.upgradeRuntime.ActiveFlow.FlowID, OptionID: option}})
}

func TestCodexSourcePickerAlwaysVisible(t *testing.T) {
	for _, showUpgrade := range []bool{false, true} {
		found := false
		for _, button := range buildUpgradeRootButtons(showUpgrade) {
			if button.Label == "Codex 来源" {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing source selector, showUpgrade=%v", showUpgrade)
		}
	}
}

func TestCodexSourcePickerAsyncAndSameCard(t *testing.T) {
	app, gateway, _ := sourcePickerFixture(t, config.CodexRuntimeSourceManaged)
	open := waitForCodexUpgradeOperation(t, gateway, func(op feishu.Operation) bool { return op.CardTitle == "选择 Codex 来源" })
	entered, release := make(chan struct{}), make(chan struct{})
	app.runCodexCapabilityPreflight = func(_ context.Context, opts codexprofile.CapabilityPreflightOptions) (codexprofile.CapabilityPreflightObservation, error) {
		if opts.BinaryPath != "existing-cli" {
			t.Errorf("target binary %q", opts.BinaryPath)
		}
		close(entered)
		<-release
		return codexprofile.CapabilityPreflightObservation{CapabilitySet: codexprofile.CodexProfileCapabilitySetV1}, nil
	}
	started := make(chan install.CodexSourceSwitchOptions, 1)
	app.startCodexSourceSwitch = func(opts install.CodexSourceSwitchOptions) error { started <- opts; return nil }
	sourcePickerClick(app, config.CodexRuntimeSourceExternal)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("preflight did not start")
	}
	progress := waitForCodexUpgradeOperation(t, gateway, func(op feishu.Operation) bool { return op.CardTitle == "正在切换 Codex 来源" })
	if progress.Kind != feishu.OperationUpdateCard || progress.MessageID != open.MessageID {
		t.Fatal("progress did not patch source owner card")
	}
	app.mu.Lock()
	if app.codexSourceSwitch == nil || !app.upgradeRuntime.StartInFlight {
		t.Fatal("missing dispatch fence")
	}
	app.mu.Unlock()
	close(release)
	select {
	case opts := <-started:
		if opts.TargetSource != "external" || opts.SourceMessageID != open.MessageID || opts.ActorUserID != "user-1" || opts.ConfigDigest == "" {
			t.Fatalf("incorrect helper context %#v", opts)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("helper did not start")
	}
}

func TestCodexSourcePickerFailedProbeKeepsConfig(t *testing.T) {
	app, gateway, path := sourcePickerFixture(t, config.CodexRuntimeSourceManaged)
	before, _ := os.ReadFile(path)
	app.runCodexCapabilityPreflight = func(context.Context, codexprofile.CapabilityPreflightOptions) (codexprofile.CapabilityPreflightObservation, error) {
		return codexprofile.CapabilityPreflightObservation{}, errors.New("unavailable CLI")
	}
	app.startCodexSourceSwitch = func(install.CodexSourceSwitchOptions) error { t.Error("helper ran after failed probe"); return nil }
	sourcePickerClick(app, config.CodexRuntimeSourceExternal)
	waitForCodexUpgradeOperation(t, gateway, func(op feishu.Operation) bool { return op.CardTitle == "Codex 来源未切换" })
	after, _ := os.ReadFile(path)
	app.mu.Lock()
	defer app.mu.Unlock()
	if string(before) != string(after) || app.codexSourceSwitch != nil || app.upgradeRuntime.StartInFlight {
		t.Fatal("failure changed config or retained dispatch fence")
	}
}

func TestCodexSourcePickerBusyIncludesInitiator(t *testing.T) {
	for _, reason := range []string{"queue", "request", "launch", "turn", "warm"} {
		t.Run(reason, func(t *testing.T) {
			app, gateway, _ := sourcePickerFixture(t, "managed")
			surface := app.surfaceByIDLocked("surface-1")
			switch reason {
			case "queue":
				surface.QueuedQueueItemIDs = []string{"queued"}
			case "request":
				surface.ActiveQueueItemID = "active"
			case "launch":
				surface.PendingHeadless = &state.HeadlessLaunchRecord{}
			case "turn":
				app.service.UpsertInstance(&state.InstanceRecord{InstanceID: "initiator", Online: true, ActiveTurnID: "turn"})
			case "warm":
				app.codexRuntimeWarmInFlight = 1
			}
			app.runCodexCapabilityPreflight = func(context.Context, codexprofile.CapabilityPreflightOptions) (codexprofile.CapabilityPreflightObservation, error) {
				t.Error("preflight ran while busy")
				return codexprofile.CapabilityPreflightObservation{}, nil
			}
			app.mu.Lock()
			events := app.handleCodexSourcePickerLocked(control.DaemonCommand{SurfaceSessionID: "surface-1", PickerID: app.upgradeRuntime.ActiveFlow.FlowID, OptionID: "external"})
			app.handleUIEventsLocked(context.Background(), events)
			app.mu.Unlock()
			waitForCodexUpgradeOperation(t, gateway, func(op feishu.Operation) bool { return op.CardTitle == "Codex 来源未切换" })
		})
	}
}

func TestCodexSourcePickerStaleOwnerAndLegacyMigration(t *testing.T) {
	app, _, _ := sourcePickerFixture(t, "legacy")
	flow := app.upgradeRuntime.ActiveFlow
	app.managedCodex.prepare = func(context.Context, string, codexmanaged.Options, []string) (codexmanaged.Release, error) {
		t.Error("unsafe legacy preparation")
		return codexmanaged.Release{}, nil
	}
	sourcePickerClick(app, "managed")
	if app.codexSourceSwitch != nil {
		t.Fatal("direct legacy to managed migration admitted")
	}
	app.HandleAction(context.Background(), control.Action{Kind: control.ActionUpgradeCommand, SurfaceSessionID: "surface-1", ChatID: "chat-1", ActorUserID: "user-1", Text: "/upgrade codex-source"})
	app.HandleAction(context.Background(), control.Action{Kind: control.ActionUpgradeOwnerFlow, SurfaceSessionID: "surface-1", ChatID: "chat-1", ActorUserID: "user-1", OwnerFlow: &control.ActionOwnerCardFlow{FlowID: flow.FlowID, OptionID: "external"}})
	if app.codexSourceSwitch != nil {
		t.Fatal("stale owner card admitted")
	}
	currentID := app.upgradeRuntime.ActiveFlow.FlowID
	app.HandleAction(context.Background(), control.Action{Kind: control.ActionUpgradeOwnerFlow, SurfaceSessionID: "surface-1", ChatID: "chat-1", ActorUserID: "user-other", OwnerFlow: &control.ActionOwnerCardFlow{FlowID: currentID, OptionID: "external"}})
	if app.codexSourceSwitch != nil {
		t.Fatal("another user admitted")
	}
}

func TestCodexSourcePickerOverrideRefuses(t *testing.T) {
	t.Setenv(config.CodexRuntimeSourceEnv, "managed")
	app, _, _ := sourcePickerFixture(t, "managed")
	sourcePickerClick(app, "external")
	if app.codexSourceSwitch != nil || app.upgradeRuntime.ActiveFlow != nil {
		t.Fatal("environment override accepted")
	}
}

func TestCodexSourceSwitchResult(t *testing.T) {
	text := buildUpgradeResultText(install.InstallState{PendingUpgrade: &install.PendingUpgrade{Phase: install.PendingUpgradePhaseCommitted, CodexSourceSwitch: &install.CodexSourceSwitch{TargetSource: "managed"}}})
	if !strings.Contains(text, "托管") || strings.Contains(text, "已升级到") {
		t.Fatalf("incorrect result %q", text)
	}
}

func TestCodexSourcePickerMenuHandoffUpdatesStampedCard(t *testing.T) {
	app, gateway, _ := sourcePickerFixture(t, "managed")
	before := len(gateway.snapshotOperations())
	applyGatewayActionForTest(context.Background(), app, control.Action{
		Kind: control.ActionUpgradeCommand, GatewayID: "app-1", SurfaceSessionID: "surface-1", ChatID: "chat-1", ActorUserID: "user-1",
		MessageID: "source-menu-card", Text: "/upgrade codex-source", LocalPageAction: true,
		Inbound: &control.ActionInboundMeta{CardDaemonLifecycleID: app.daemonLifecycleID},
	})
	ops := gateway.snapshotOperations()
	if len(ops) != before+1 || ops[len(ops)-1].Kind != feishu.OperationUpdateCard || ops[len(ops)-1].MessageID != "source-menu-card" || ops[len(ops)-1].CardTitle != "选择 Codex 来源" {
		t.Fatal("source selector did not take over same menu card")
	}
}

func TestCodexSourceSwitchObservesHelperFailureWithoutStaleUnfreeze(t *testing.T) {
	for _, phase := range []string{install.PendingUpgradePhaseFailed, install.PendingUpgradePhaseRolledBack, install.PendingUpgradePhaseCommitted, "stale", "mismatch"} {
		t.Run(phase, func(t *testing.T) {
			app, _, _ := sourcePickerFixture(t, "managed")
			flow := app.upgradeRuntime.ActiveFlow
			app.mu.Lock()
			stateValue, _, err := app.loadUpgradeStateLocked(true)
			app.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			requested := now
			journalPhase := phase
			if phase == "stale" {
				requested = now.Add(-time.Hour)
				journalPhase = install.PendingUpgradePhaseFailed
			}
			if phase == "mismatch" {
				journalPhase = install.PendingUpgradePhaseFailed
			}
			stateValue.PendingUpgrade = &install.PendingUpgrade{Phase: journalPhase, RequestedAt: &requested, CodexSourceSwitch: &install.CodexSourceSwitch{TargetSource: "external", ConfigDigest: "digest"}}
			if err := install.WriteState(stateValue.StatePath, stateValue); err != nil {
				t.Fatal(err)
			}
			app.admin.loadConfig = func() (config.LoadedAppConfig, error) {
				cfg := config.DefaultAppConfig()
				cfg.Wrapper.CodexRuntime.Source = "managed"
				if phase == "mismatch" {
					cfg.Wrapper.CodexRuntime.Source = "external"
				}
				return config.LoadedAppConfig{Config: cfg}, nil
			}
			app.codexSourceSwitch = &codexSourceSwitchRun{flowID: flow.FlowID, cfg: app.headlessRuntime, admittedAt: now.Add(-time.Second), helperStarted: true, recoveryNoticeAt: now.Add(4 * time.Minute), opts: install.CodexSourceSwitchOptions{TargetSource: "external", ConfigDigest: "digest"}}
			app.upgradeRuntime.StartInFlight = true
			app.mu.Lock()
			app.observeCodexSourceSwitchLocked(context.Background(), now)
			app.mu.Unlock()
			if (app.codexSourceSwitch == nil) != (phase == install.PendingUpgradePhaseFailed || phase == install.PendingUpgradePhaseRolledBack) {
				t.Fatalf("unsafe fence state, phase=%s", phase)
			}
		})
	}
}
