package daemon

import (
	"context"
	"errors"
	"github.com/kxn/codex-remote-feishu/internal/app/codexprofile"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/app/codexmanaged"
	"github.com/kxn/codex-remote-feishu/internal/app/codexupgrade"
	"github.com/kxn/codex-remote-feishu/internal/app/daemon/codexupgraderuntime"
	"github.com/kxn/codex-remote-feishu/internal/app/sharedcodex"
	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

type ownerStub struct {
	status    sharedcodex.Status
	request   sharedcodex.Upgrade
	err       error
	onUpgrade func()
}

func TestSharedCodexUpgradeLookupUsesCapturedEnvironmentAndStableChannel(t *testing.T) {
	a, _ := enrolledSharedFixture(t)
	env := []string{"HTTPS_PROXY=http://127.0.0.1:20171", "NO_PROXY=localhost"}
	a.headlessRuntime.BaseEnv = env
	a.headlessRuntime.CodexRuntime.Channel = "alpha"
	a.headlessRuntime.CodexRuntime.Version = "0.100.0"
	a.managedCodex.latest = func(_ context.Context, opts codexmanaged.Options, actual []string) (string, error) {
		if opts.Channel != "stable" || opts.Version != "" || opts.UpgradeProxyMode != "system" || !reflect.DeepEqual(actual, env) {
			t.Fatalf("shared lookup inherited private policy or lost model environment: %+v %v", opts, actual)
		}
		return "0.163.0", nil
	}
	a.configureManagedCodexUpgrade()
	check, err := a.checkStandaloneCodexUpgrade(context.Background(), "surface-1")
	if err != nil || check.LatestVersion != "0.163.0" || !check.HasUpdate {
		t.Fatalf("shared version check: %+v %v", check, err)
	}
}

func TestSharedCodexUpgradeLookupActuallyContactsCapturedProxy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows system mode reads Windows settings; covered with injected settings in upgradeproxy")
	}
	contacted := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contacted <- r.Method + " " + r.Host
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	a, _ := enrolledSharedFixture(t)
	a.headlessRuntime.BaseEnv = []string{"HTTPS_PROXY=" + proxy.URL}
	a.configureManagedCodexUpgrade()
	if _, err := a.checkStandaloneCodexUpgrade(context.Background(), "surface-1"); err == nil {
		t.Fatal("expected controlled proxy failure")
	}
	select {
	case got := <-contacted:
		if got != "CONNECT registry.npmjs.org:443" {
			t.Fatalf("unexpected proxy request: %s", got)
		}
	default:
		t.Fatal("shared lookup bypassed captured proxy")
	}
}

func (o *ownerStub) Status(context.Context) (sharedcodex.Status, error) { return o.status, nil }
func (o *ownerStub) Upgrade(_ context.Context, u sharedcodex.Upgrade) (sharedcodex.Status, error) {
	o.request = u
	if o.onUpgrade != nil {
		o.onUpgrade()
	}
	if o.err == nil {
		o.status.Version = u.Version
		o.status.Generation++
	}
	return o.status, o.err
}

func enrolledSharedFixture(t *testing.T) (*App, *ownerStub) {
	a := sharedCodexDaemonFixture(t)
	o := &ownerStub{status: sharedcodex.Status{Generation: 1, UpgradeProxySupported: true, Version: "0.162.1", PreviousVersion: "0.161.0", State: "running"}}
	a.codexUpgradeRuntime.SharedOwner = func(_ string, endpoint string) (sharedcodex.Maintenance, error) {
		if endpoint != a.headlessRuntime.CodexAppServerURL {
			t.Fatal("wrong backend owner")
		}
		return o, nil
	}
	a.codexUpgradeRuntime.LatestLookup = func(context.Context) (string, error) { return "0.163.0", nil }
	return a, o
}

func TestSharedCodexUpgradeChecksActualOwnerRatherThanPrivateCLI(t *testing.T) {
	a, o := enrolledSharedFixture(t)
	check, err := a.checkStandaloneCodexUpgrade(context.Background(), "surface-1")
	if err != nil {
		t.Fatal(err)
	}
	if check.Installation.SourceKind != codexupgrade.SourceSharedBackend || check.CurrentVersion != o.status.Version || !check.CanUpgrade || !check.CanRollback {
		t.Fatalf("wrong actual owner projection: %+v", check)
	}
	if a.headlessRuntime.CodexRealBinary != "missing-private-cli" {
		t.Fatal("check changed configured CLI")
	}
	o.status.Version = "0.164.0-alpha.1"
	check, err = a.checkStandaloneCodexUpgrade(context.Background(), "surface-1")
	if err != nil || check.HasUpdate {
		t.Fatalf("stable check must not offer an older release: %+v %v", check, err)
	}
	for _, busy := range []bool{true, false} {
		o.status.Busy = busy
		o.status.State = "failed"
		if _, err := a.inspectSharedCodexInstallation(context.Background()); err == nil {
			t.Fatal("unhealthy owner marked upgradeable")
		}
	}
}

func TestSharedCodexUpgradeWithoutBindingsDoesNotRestartIdlePool(t *testing.T) {
	a, o := enrolledSharedFixture(t)
	tx := &codexupgraderuntime.Transaction{ID: "shared-1", UpgradeProxyMode: "direct", CurrentVersion: "0.162.1", TargetVersion: "0.163.0"}
	a.codexUpgradeRuntime.Active = tx
	a.managedCodex.restart = func(context.Context, []string) error { t.Fatal("must not restart obsolete wrapper IDs"); return nil }
	var result error
	a.runSharedCodexUpgrade(context.Background(), tx, func(err error) { result = err })
	if result != nil || a.codexUpgradeRuntime.Active != nil {
		t.Fatalf("upgrade did not finish: %v", result)
	}
	if o.request.Version != tx.TargetVersion || o.request.ExpectedVersion != tx.CurrentVersion || o.request.UpgradeProxyMode != "direct" {
		t.Fatalf("unfrozen target: %+v", o.request)
	}
}

func TestSharedCodexUpgradeFailedRecoveryKeepsInputGate(t *testing.T) {
	a, o := enrolledSharedFixture(t)
	o.err = errors.New("both versions failed")
	o.status.State = "failed"
	tx := &codexupgraderuntime.Transaction{ID: "shared-2", CurrentVersion: "0.162.1", TargetVersion: "0.163.0"}
	a.codexUpgradeRuntime.Active = tx
	a.managedCodex.restart = func(context.Context, []string) error { t.Fatal("must not reconnect to failed backend"); return nil }
	var result error
	a.runSharedCodexUpgrade(context.Background(), tx, func(err error) { result = err })
	if result == nil || !tx.RecoveryRequired || a.codexUpgradeRuntime.Active != tx {
		t.Fatal("unconfirmed maintenance released input gate")
	}
}

func TestSharedCodexUpgradeNeverRestartsClaudeOrOpenCode(t *testing.T) {
	a, _ := enrolledSharedFixture(t)
	a.headlessRuntime.CodexRuntime.Source = config.CodexRuntimeSourceExternal
	for _, backend := range []agentproto.Backend{agentproto.BackendClaude, agentproto.BackendOpenCode} {
		if a.codexUpgradeAffectsInstanceLocked(&state.InstanceRecord{Managed: true, Source: "headless", Backend: backend}) {
			t.Fatalf("shared Codex upgrade affects %s", backend)
		}
	}
}

func TestSharedCodexUpgradeRejectsOwnerWithoutProxyCapability(t *testing.T) {
	a, o := enrolledSharedFixture(t)
	o.status.UpgradeProxySupported = false
	if _, err := a.inspectSharedCodexInstallation(context.Background()); err == nil || !strings.Contains(err.Error(), "升级代理") {
		t.Fatalf("unsupported owner accepted: %v", err)
	}
	if o.request.Version != "" {
		t.Fatal("unsupported owner received maintenance")
	}
}

func TestManagedCodexUpgradeProxyPolicyDoesNotChangeProbeEnvironment(t *testing.T) {
	a, s := newManagedCodexTestApp(t)
	cfg := config.DefaultAppConfig()
	cfg.Upgrade.ProxyMode = "direct"
	a.admin.loadConfig = func() (config.LoadedAppConfig, error) { return config.LoadedAppConfig{Config: cfg}, nil }
	env := []string{"HTTPS_PROXY=http://model:4567", "CODEX_HOME=private"}
	a.headlessRuntime.BaseEnv = env
	release := codexmanaged.Release{Version: "0.163.0", Binary: "candidate"}
	s.Candidate = &release
	a.managedCodex.prepare = func(_ context.Context, _ string, opts codexmanaged.Options, actual []string) (codexmanaged.Release, error) {
		if opts.UpgradeProxyMode != "direct" || !reflect.DeepEqual(actual, env) {
			t.Fatalf("policy or runtime env lost: %+v %v", opts, actual)
		}
		return release, nil
	}
	a.runCodexCapabilityPreflight = func(_ context.Context, opts codexprofile.CapabilityPreflightOptions) (codexprofile.CapabilityPreflightObservation, error) {
		if !reflect.DeepEqual(opts.Env, env) {
			t.Fatalf("model proxy was replaced: %v", opts.Env)
		}
		return codexprofile.CapabilityPreflightObservation{CapabilitySet: codexprofile.CodexProfileCapabilitySetV1}, nil
	}
	if _, err := a.prepareManagedCodex(context.Background(), a.headlessRuntime, "0.163.0", true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.headlessRuntime.BaseEnv, env) {
		t.Fatal("native base env modified")
	}
}
