package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/app/codexmanaged"
	"github.com/kxn/codex-remote-feishu/internal/app/codexprofile"
	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func newManagedCodexTestApp(t *testing.T) (*App, *codexmanaged.State) {
	t.Helper()
	a, _ := newUpgradeTestApp(t, newLifecycleGateway())
	a.headlessRuntime.CodexRuntime = config.CodexRuntimeSettings{Source: config.CodexRuntimeSourceManaged, Channel: "stable"}
	a.headlessRuntime.CodexRealBinary = filepath.Join(t.TempDir(), "missing-app-cli")
	cfg := config.DefaultAppConfig()
	cfg.Wrapper.CodexRuntime = a.headlessRuntime.CodexRuntime
	a.admin.loadConfig = func() (config.LoadedAppConfig, error) { return config.LoadedAppConfig{Config: cfg}, nil }
	s := &codexmanaged.State{Failed: map[string]codexmanaged.Failure{}}
	a.managedCodex.inspect = func(string) (codexmanaged.State, error) { return *s, nil }
	a.managedCodex.activate = func(_ context.Context, _, v string) (codexmanaged.Release, error) {
		r := codexmanaged.Release{Version: v, Binary: filepath.Join(a.headlessRuntime.Paths.StateDir, v, "codex")}
		s.Previous, s.Active, s.Candidate = s.Active, &r, nil
		return r, nil
	}
	a.managedCodex.reject = func(_ context.Context, _, v, reason string) error {
		s.Failed[v] = codexmanaged.Failure{Reason: reason}
		s.Candidate = nil
		return nil
	}
	a.runCodexCapabilityPreflight = func(context.Context, codexprofile.CapabilityPreflightOptions) (codexprofile.CapabilityPreflightObservation, error) {
		return codexprofile.CapabilityPreflightObservation{CapabilitySet: codexprofile.CodexProfileCapabilitySetV1}, nil
	}
	a.configureManagedCodexUpgrade()
	return a, s
}

func TestManagedCodexBootstrapWithoutConfiguredCLI(t *testing.T) {
	a, s := newManagedCodexTestApp(t)
	a.managedCodex.prepare = func(_ context.Context, _ string, opts codexmanaged.Options, _ []string) (codexmanaged.Release, error) {
		if opts.Channel != "stable" || opts.RetryFailed {
			t.Fatalf("bootstrap options: %#v", opts)
		}
		return codexmanaged.Release{Version: "0.162.1", Binary: "official-private-cli"}, nil
	}
	a.warmManagedCodex(context.Background(), true, func(context.Context, string, string, []string) (string, error) {
		t.Fatal("managed bootstrap invoked the legacy App resolver")
		return "", nil
	})
	if s.Active == nil || s.Active.Version != "0.162.1" || a.headlessRuntime.CodexRealBinary != s.Active.Binary {
		t.Fatalf("bootstrap failed: %#v", s)
	}
}

func TestManagedCodexOfflineStartupKeepsActiveSelection(t *testing.T) {
	a, s := newManagedCodexTestApp(t)
	s.Active = &codexmanaged.Release{Version: "0.161.0", Binary: "selected-private-cli"}
	s.Candidate = &codexmanaged.Release{Version: "0.162.1", Binary: "newer-candidate"}
	a.managedCodex.resolve = func(context.Context, string, []string) (codexmanaged.Release, error) { return *s.Active, nil }
	a.managedCodex.prepare = func(context.Context, string, codexmanaged.Options, []string) (codexmanaged.Release, error) {
		t.Fatal("startup or disabled automatic update accessed registry")
		return codexmanaged.Release{}, nil
	}
	a.warmManagedCodex(context.Background(), true, nil)
	a.warmManagedCodex(context.Background(), false, nil)
	a.maybeActivateManagedCodexCandidate(context.Background())
	if a.headlessRuntime.CodexRealBinary != "selected-private-cli" || s.Active.Version != "0.161.0" {
		t.Fatalf("unrequested selection changed: %#v", s)
	}
}

func TestManagedCodexPreflightRejectsOnlyCandidate(t *testing.T) {
	a, s := newManagedCodexTestApp(t)
	s.Active = &codexmanaged.Release{Version: "0.161.0", Binary: "good-cli"}
	a.managedCodex.prepare = func(context.Context, string, codexmanaged.Options, []string) (codexmanaged.Release, error) {
		return codexmanaged.Release{Version: "0.162.1", Binary: "bad-cli"}, nil
	}
	a.runCodexCapabilityPreflight = func(context.Context, codexprofile.CapabilityPreflightOptions) (codexprofile.CapabilityPreflightObservation, error) {
		return codexprofile.CapabilityPreflightObservation{}, errors.New("unsupported protocol")
	}
	if _, err := a.prepareManagedCodex(context.Background(), a.headlessRuntime, "", false); err == nil {
		t.Fatal("bad candidate passed probe")
	}
	if s.Active.Version != "0.161.0" || s.Failed["0.162.1"].Reason == "" {
		t.Fatalf("active not preserved or rejection missing: %#v", s)
	}
	a.managedCodex.prepare = func(context.Context, string, codexmanaged.Options, []string) (codexmanaged.Release, error) {
		return *s.Active, nil
	}
	if _, err := a.prepareManagedCodex(context.Background(), a.headlessRuntime, "", false); err != nil {
		t.Fatalf("idempotent active prepare: %v", err)
	}
	if _, failed := s.Failed[s.Active.Version]; failed {
		t.Fatal("idempotent prepare rejected live CLI")
	}
}

func TestManagedCodexScopeExcludesOtherBackendsAndIDE(t *testing.T) {
	a, _ := newManagedCodexTestApp(t)
	for _, tc := range []struct {
		id, source    string
		backend       agentproto.Backend
		managed, want bool
	}{
		{"codex", "headless", agentproto.BackendCodex, true, true},
		{"claude", "headless", agentproto.BackendClaude, true, false},
		{"opencode", "headless", agentproto.BackendOpenCode, true, false},
		{"external", "headless", agentproto.BackendCodex, false, false},
		{"ide", "vscode", agentproto.BackendCodex, true, false},
	} {
		inst := &state.InstanceRecord{InstanceID: tc.id, Source: tc.source, Backend: tc.backend, Managed: tc.managed, Online: true, ActiveTurnID: "busy"}
		a.service.UpsertInstance(inst)
		if got := a.codexUpgradeAffectsInstanceLocked(inst); got != tc.want {
			t.Errorf("scope %s = %v", tc.id, got)
		}
	}
	if reasons := a.codexUpgradeBusyReasonsLocked(""); len(reasons) != 1 || reasons[0].InstanceID != "codex" {
		t.Fatalf("unrelated sessions blocked managed upgrade: %#v", reasons)
	}
}

func TestManagedCodexAutomaticCheckUsesStagedCandidateAndHonorsPolicy(t *testing.T) {
	a, s := newManagedCodexTestApp(t)
	a.headlessRuntime.CodexRuntime.AutoUpdate = true
	s.Active = &codexmanaged.Release{Version: "0.161.0"}
	s.Candidate = &codexmanaged.Release{Version: "0.162.1", Channel: "stable"}
	a.managedCodexCandidateVersion = s.Candidate.Version
	a.managedCodex.latest = func(context.Context, codexmanaged.Options, []string) (string, error) {
		t.Fatal("idle activation required registry")
		return "", nil
	}
	req := codexUpgradeStartRequest{TargetVersion: "0.162.1", Automatic: true}
	if check, err := a.codexUpgradeRequestCheck(context.Background(), req); err != nil || !check.HasUpdate {
		t.Fatalf("staged activation check: %#v %v", check, err)
	}
	s.Failed[req.TargetVersion] = codexmanaged.Failure{Reason: "bad"}
	if _, err := a.codexUpgradeRequestCheck(context.Background(), req); err == nil {
		t.Fatal("automatic retry of rejected release allowed")
	}
	delete(s.Failed, req.TargetVersion)
	s.Candidate.Channel = "alpha"
	if _, err := a.codexUpgradeRequestCheck(context.Background(), req); err == nil {
		t.Fatal("automatic stable policy selected an alpha candidate")
	}
	s.Candidate.Channel = "stable"
	a.managedCodexCandidateVersion = ""
	if _, err := a.codexUpgradeRequestCheck(context.Background(), req); err == nil {
		t.Fatal("CLI-only candidate automatically activated")
	}
	a.managedCodexCandidateVersion = req.TargetVersion
	s.Active.Version = "0.163.0"
	if _, err := a.codexUpgradeRequestCheck(context.Background(), req); err == nil {
		t.Fatal("automatic update downgraded an active release")
	}
	s.Active.Version = "0.161.0"
	a.headlessRuntime.CodexRuntime.Version = "0.161.0"
	if _, err := a.codexUpgradeRequestCheck(context.Background(), req); err == nil {
		t.Fatal("pin ignored")
	}
	// A busy turn leaves the candidate staged without starting an upgrade.
	a.headlessRuntime.CodexRuntime.Version = ""
	a.service.UpsertInstance(&state.InstanceRecord{InstanceID: "busy", Source: "headless", Backend: agentproto.BackendCodex, Managed: true, Online: true, ActiveTurnID: "turn"})
	a.maybeActivateManagedCodexCandidate(context.Background())
	if a.codexUpgradeRuntime.Active != nil || s.Candidate == nil {
		t.Fatal("busy turn activated staged candidate")
	}
}
