package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/app/codexmanaged"
	"github.com/kxn/codex-remote-feishu/internal/app/codexprofile"
	"github.com/kxn/codex-remote-feishu/internal/app/install"
	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	relayruntime "github.com/kxn/codex-remote-feishu/internal/runtime"
)

func sharedCodexDaemonFixture(t *testing.T) *App {
	t.Helper()
	root := evalSymlinkForTest(t, t.TempDir())
	cfg := config.DefaultAppConfig()
	cfg.Wrapper.CodexRealBinary = "missing-private-cli"
	cfg.Wrapper.CodexAppServerURL = "ws://127.0.0.1:34567"
	configPath := filepath.Join(root, "config.json")
	if err := config.WriteAppConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	app := New(":0", ":0", &recordingGateway{}, agentproto.ServerIdentity{})
	app.SetHeadlessRuntime(HeadlessRuntimeConfig{
		BinaryPath: filepath.Join(root, "codex-remote"), CodexRealBinary: cfg.Wrapper.CodexRealBinary,
		CodexAppServerURL: cfg.Wrapper.CodexAppServerURL,
		CodexRuntime:      config.CodexRuntimeSettings{Source: config.CodexRuntimeSourceManaged, AutoUpdate: true},
		ConfigPath:        configPath, LaunchArgs: []string{"app-server"},
		BaseEnv: []string{config.CodexAppServerURLEnv + "=ws://127.0.0.1:1"},
		Paths:   relayruntime.Paths{LogsDir: root, StateDir: root},
	})
	app.admin.loadConfig = func() (config.LoadedAppConfig, error) {
		return config.LoadedAppConfig{Config: cfg, Path: configPath}, nil
	}
	app.runCodexNativeConfigProbe = func(_ context.Context, opts codexprofile.NativeConfigProbeOptions) (codexprofile.NativeConfigObservation, error) {
		if opts.ServerURL != cfg.Wrapper.CodexAppServerURL {
			t.Fatalf("native evidence from wrong backend: %q", opts.ServerURL)
		}
		return codexprofile.NativeConfigObservation{ModelProviderID: "shared-test", ModelEndpoint: "http://127.0.0.1:9/v1"}, nil
	}
	app.ensureCodexNativeConnectionEvidence(context.Background())
	return app
}

func TestSharedCodexDaemonUsesObservedNativeProjection(t *testing.T) {
	app := sharedCodexDaemonFixture(t)
	env, args, projection, err := app.applyCodexHeadlessProfileConfigLocked(app.headlessRuntime.BaseEnv, []string{"app-server"}, agentproto.BackendCodex, state.NativeCodexProfileID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 1 || args[0] != "app-server" || !containsEnvEntry(env, config.CodexAppServerURLEnv+"=ws://127.0.0.1:34567") {
		t.Fatalf("shared launch contract: args=%v env=%v", args, env)
	}
	if projection.Connection.CapabilitySet != codexprofile.SharedNativeCapabilitySetV1 || projection.Connection.ModelProviderID != "shared-test" || projection.Connection.ModelEndpointID != "http://127.0.0.1:9/v1" {
		t.Fatalf("incorrect actual backend evidence: %+v", projection.Connection)
	}
	if len(projection.Launch.CLIOverrides) != 0 || len(projection.Launch.SecretChildEnv) != 0 || len(projection.Launch.ManagedFiles) != 0 {
		t.Fatal("shared projection must not create private launch material")
	}
	for _, id := range []string{state.OAuthCodexProfileID, "custom-api"} {
		_, _, _, err := app.applyCodexHeadlessProfileConfigLocked(nil, nil, agentproto.BackendCodex, id, nil)
		if codexprofile.RuntimeErrorCode(err) != codexprofile.ErrorSharedBackendProfileUnsupported {
			t.Fatalf("profile %q accepted: %v", id, err)
		}
	}
	app.codexNativeConnection.probeSucceeded = false
	if _, _, _, err := app.applyCodexHeadlessProfileConfigLocked(nil, nil, agentproto.BackendCodex, state.NativeCodexProfileID, nil); codexprofile.RuntimeErrorCode(err) != codexprofile.ErrorCodexProbeUnavailable {
		t.Fatalf("unproven backend accepted: %v", err)
	}
}

func TestSharedCodexDaemonSkipsPrivateLifecycle(t *testing.T) {
	app := sharedCodexDaemonFixture(t)
	app.runCodexCapabilityPreflight = func(context.Context, codexprofile.CapabilityPreflightOptions) (codexprofile.CapabilityPreflightObservation, error) {
		t.Fatal("shared connection must not run private CLI capability preflight")
		return codexprofile.CapabilityPreflightObservation{}, nil
	}
	app.ensureCodexRuntimeCapability(context.Background())
	app.warmManagedCodex(context.Background(), true, func(context.Context, string, string, []string) (string, error) {
		t.Fatal("shared connection must not install/update a CLI")
		return "", nil
	})
	app.managedCodex.inspect = func(string) (codexmanaged.State, error) {
		t.Fatal("shared connection must not activate a managed CLI candidate")
		return codexmanaged.State{}, nil
	}
	app.maybeActivateManagedCodexCandidate(context.Background())
}

func TestSharedCodexDaemonLaunchesExactThreadWithoutProfileOverrides(t *testing.T) {
	app := sharedCodexDaemonFixture(t)
	app.service.MaterializeSurfaceResumeWithCodexProfile("surface-1", "", "chat-1", "user-1", "normal", agentproto.BackendCodex, state.NativeCodexProfileID, "", "", "")
	var captured relayruntime.HeadlessLaunchOptions
	app.startHeadless = func(opts relayruntime.HeadlessLaunchOptions) (int, error) {
		captured = opts
		return 4324, nil
	}
	command := control.DaemonCommand{Kind: control.DaemonCommandStartHeadless, SurfaceSessionID: "surface-1", InstanceID: "inst-shared", ThreadID: "shared-thread", ThreadCWD: evalSymlinkForTest(t, t.TempDir()), Backend: agentproto.BackendCodex, CodexProfileID: state.NativeCodexProfileID}
	authorizePendingHeadlessForTest(t, app, command)
	app.startManagedHeadless(command)
	if !containsEnvEntry(captured.Env, config.ResumeThreadIDEnv+"=shared-thread") || !containsEnvEntry(captured.Env, config.CodexAppServerURLEnv+"=ws://127.0.0.1:34567") {
		t.Fatalf("missing frozen scope or endpoint: %v", captured.Env)
	}
	if len(captured.Args) != 1 || captured.Args[0] != "app-server" {
		t.Fatalf("private launch overrides applied: %v", captured.Args)
	}
}

func TestSharedCodexDaemonBlocksSourceAndCliUpgrade(t *testing.T) {
	app := sharedCodexDaemonFixture(t)
	app.managedCodex.inspect = func(string) (codexmanaged.State, error) {
		t.Fatal("shared backend must reject direct CLI upgrade before inspecting local releases")
		return codexmanaged.State{}, nil
	}
	for _, req := range []codexUpgradeStartRequest{{}, {Rollback: true}, {Automatic: true}} {
		if err := app.startStandaloneCodexUpgrade(context.Background(), req); err == nil {
			t.Fatal("shared backend accepted a direct CLI upgrade")
		}
	}
	if _, err := app.inspectStandaloneCodexInstallation(context.Background()); err == nil {
		t.Fatal("external shared backend presented as local upgradeable CLI")
	}
	events := app.openCodexSourcePickerLocked(control.DaemonCommand{SurfaceSessionID: "surface-1"}, install.InstallState{})
	if len(events) != 1 || events[0].Notice == nil || events[0].Notice.Code != "codex_shared_backend_external" || app.codexSourceSwitch != nil || app.upgradeRuntime.ActiveFlow != nil {
		t.Fatalf("unexpected source transaction: %+v", events)
	}
	if app.standaloneCodexUpgradeVisibleLocked() {
		t.Fatal("shared backend CLI upgrade must be hidden")
	}
	profiles := app.materializeCodexProfileSummariesLocked(config.DefaultAppConfig())
	if len(profiles) != 1 || profiles[0].ID != state.NativeCodexProfileID || !strings.Contains(profiles[0].Name, "共享") {
		t.Fatalf("incorrect shared profile catalog: %+v", profiles)
	}
	if _, ok := app.beginCodexOAuthProbeLocked(false); ok {
		t.Fatal("private OAuth probe started for shared backend")
	}
}

func TestSharedCodexDaemonUnavailableProbeCanRetry(t *testing.T) {
	app := sharedCodexDaemonFixture(t)
	app.codexNativeConnection.checked = false
	app.runCodexNativeConfigProbe = func(context.Context, codexprofile.NativeConfigProbeOptions) (codexprofile.NativeConfigObservation, error) {
		return codexprofile.NativeConfigObservation{}, errors.New("offline")
	}
	app.ensureCodexNativeConnectionEvidence(context.Background())
	if app.codexNativeConnection.probeSucceeded {
		t.Fatal("offline shared endpoint marked available")
	}
	app.codexNativeConnection.checked = false
	app.runCodexNativeConfigProbe = func(context.Context, codexprofile.NativeConfigProbeOptions) (codexprofile.NativeConfigObservation, error) {
		return codexprofile.NativeConfigObservation{ModelProviderID: "restored"}, nil
	}
	app.ensureCodexNativeConnectionEvidence(context.Background())
	if !app.codexNativeConnection.probeSucceeded || app.codexNativeConnection.evidence.ModelProviderID != "restored" {
		t.Fatal("shared evidence did not recover")
	}
	if _, err := os.Stat(app.headlessRuntime.ConfigPath); err != nil {
		t.Fatal(err)
	}
}
