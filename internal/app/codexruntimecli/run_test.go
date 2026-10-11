package codexruntimecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/codexmanaged"
	"github.com/kxn/codex-remote-feishu/internal/app/codexprofile"
	relayruntime "github.com/kxn/codex-remote-feishu/internal/runtime"
)

func testPaths(t *testing.T) relayruntime.Paths {
	t.Helper()
	root := t.TempDir()
	return relayruntime.Paths{StateDir: root, DaemonLockFile: filepath.Join(root, "relayd.lock")}
}

func TestStatusReadsOnlyMetadata(t *testing.T) {
	paths := testPaths(t)
	var stdout bytes.Buffer
	ops := operations{inspect: func(root string) (codexmanaged.State, error) {
		if root != filepath.Join(paths.StateDir, "codex-runtime") {
			t.Fatalf("root = %q", root)
		}
		return codexmanaged.State{SchemaVersion: 1, Active: &codexmanaged.Release{Version: "1.2.3"}}, nil
	}}
	if err := run(context.Background(), []string{"status"}, &stdout, &bytes.Buffer{}, "test", paths, nil, ops); err != nil {
		t.Fatal(err)
	}
	var state codexmanaged.State
	if err := json.Unmarshal(stdout.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Active == nil || state.Active.Version != "1.2.3" {
		t.Fatalf("state = %+v", state)
	}
}

func TestExpiredProbeStillPersistsCandidateRejection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rejected := false
	err := probeCandidate(ctx, t.TempDir(), codexmanaged.Release{Version: "1.2.3", Binary: "candidate"}, nil, "test", operations{
		probe: func(context.Context, codexprofile.CapabilityPreflightOptions) (codexprofile.CapabilityPreflightObservation, error) {
			cancel()
			return codexprofile.CapabilityPreflightObservation{}, context.DeadlineExceeded
		},
		reject: func(recoveryCtx context.Context, _, _, _ string) error {
			if recoveryCtx.Err() != nil {
				t.Fatal("rejection used expired probe context")
			}
			if _, bounded := recoveryCtx.Deadline(); !bounded {
				t.Fatal("unbounded rejection")
			}
			rejected = true
			return nil
		},
	})
	if err == nil || !rejected {
		t.Fatalf("probe rejection = %v %v", err, rejected)
	}
}

func TestPrepareChecksCandidateAndRecordsFailure(t *testing.T) {
	paths := testPaths(t)
	release := codexmanaged.Release{Version: "1.2.3", Binary: "isolated-codex"}
	var rejected bool
	ops := operations{
		prepare: func(ctx context.Context, _ string, opts codexmanaged.Options, env []string) (codexmanaged.Release, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 3*time.Minute {
				t.Fatal("missing operation timeout")
			}
			if opts.Channel != "alpha" || opts.Version != "1.2.3" || !opts.RetryFailed {
				t.Fatalf("options = %+v", opts)
			}
			if len(env) != 1 || env[0] != "TEST_PROBE_ENV=value" {
				t.Fatalf("env = %v", env)
			}
			return release, nil
		},
		inspect: func(string) (codexmanaged.State, error) { return codexmanaged.State{Candidate: &release}, nil },
		probe: func(_ context.Context, opts codexprofile.CapabilityPreflightOptions) (codexprofile.CapabilityPreflightObservation, error) {
			if opts.BinaryPath != release.Binary || opts.Version != "remote-test" {
				t.Fatalf("probe = %+v", opts)
			}
			return codexprofile.CapabilityPreflightObservation{}, errors.New("protocol mismatch")
		},
		reject: func(_ context.Context, _ string, version, reason string) error {
			rejected = version == release.Version && strings.Contains(reason, "protocol mismatch")
			return nil
		},
	}
	err := run(context.Background(), []string{"prepare", "-channel", "alpha", "-version", "1.2.3", "-retry-failed"}, &bytes.Buffer{}, &bytes.Buffer{}, "remote-test", paths, []string{"TEST_PROBE_ENV=value"}, ops)
	if err == nil || !rejected {
		t.Fatalf("err = %v, rejected = %v", err, rejected)
	}
}

func TestActivationRefusesRunningDaemonBeforeProbing(t *testing.T) {
	paths := testPaths(t)
	lock, err := relayruntime.AcquireLock(context.Background(), paths.DaemonLockFile, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	err = run(context.Background(), []string{"activate", "-root", t.TempDir()}, &bytes.Buffer{}, &bytes.Buffer{}, "test", paths, nil, operations{})
	if !errors.Is(err, relayruntime.ErrLockHeld) {
		t.Fatalf("err = %v", err)
	}
}

func TestActivationGuardsDaemonThroughoutProbe(t *testing.T) {
	paths := testPaths(t)
	release := codexmanaged.Release{Version: "1.2.3", Binary: "codex"}
	activated := false
	ops := operations{
		inspect: func(string) (codexmanaged.State, error) { return codexmanaged.State{Candidate: &release}, nil },
		probe: func(ctx context.Context, _ codexprofile.CapabilityPreflightOptions) (codexprofile.CapabilityPreflightObservation, error) {
			if _, err := relayruntime.AcquireLock(ctx, paths.DaemonLockFile, false); !errors.Is(err, relayruntime.ErrLockHeld) {
				t.Fatalf("guard missing during probe: %v", err)
			}
			return codexprofile.CapabilityPreflightObservation{}, nil
		},
		activate: func(_ context.Context, _ string, version string) (codexmanaged.Release, error) {
			activated = version == release.Version
			return release, nil
		},
	}
	if err := run(context.Background(), []string{"activate", "-version", "1.2.3"}, &bytes.Buffer{}, &bytes.Buffer{}, "test", paths, nil, ops); err != nil {
		t.Fatal(err)
	}
	if !activated {
		t.Fatal("candidate was not activated")
	}
	lock, err := relayruntime.AcquireLock(context.Background(), paths.DaemonLockFile, false)
	if err != nil {
		t.Fatalf("guard not released: %v", err)
	}
	lock.Release()
}

func TestActivationRejectsOtherVersionWithoutProbing(t *testing.T) {
	paths := testPaths(t)
	ops := operations{inspect: func(string) (codexmanaged.State, error) {
		return codexmanaged.State{Candidate: &codexmanaged.Release{Version: "1.2.3"}}, nil
	}}
	err := run(context.Background(), []string{"activate", "-version", "2.0.0"}, &bytes.Buffer{}, &bytes.Buffer{}, "test", paths, nil, ops)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareCurrentReleaseDoesNotRejectActiveRuntime(t *testing.T) {
	paths := testPaths(t)
	release := codexmanaged.Release{Version: "1.2.3"}
	ops := operations{
		prepare: func(context.Context, string, codexmanaged.Options, []string) (codexmanaged.Release, error) {
			return release, nil
		},
		inspect: func(string) (codexmanaged.State, error) { return codexmanaged.State{Active: &release}, nil },
	}
	if err := run(context.Background(), []string{"prepare"}, &bytes.Buffer{}, &bytes.Buffer{}, "test", paths, nil, ops); err != nil {
		t.Fatal(err)
	}
}

func TestFlagsRejectInvalidInput(t *testing.T) {
	for _, args := range [][]string{{}, {"update"}, {"prepare", "-channel", "beta"}, {"status", "unexpected"}, {"activate", "-retry-failed"}, {"status", "-root", " "}} {
		if err := run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}, "test", testPaths(t), nil, operations{}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
