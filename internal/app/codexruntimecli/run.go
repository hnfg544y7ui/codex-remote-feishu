package codexruntimecli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/codexmanaged"
	"github.com/kxn/codex-remote-feishu/internal/app/codexprofile"
	relayruntime "github.com/kxn/codex-remote-feishu/internal/runtime"
)

type operations struct {
	inspect  func(string) (codexmanaged.State, error)
	prepare  func(context.Context, string, codexmanaged.Options, []string) (codexmanaged.Release, error)
	activate func(context.Context, string, string) (codexmanaged.Release, error)
	reject   func(context.Context, string, string, string) error
	probe    func(context.Context, codexprofile.CapabilityPreflightOptions) (codexprofile.CapabilityPreflightObservation, error)
}

// Run exposes preparation and explicitly offline activation. Online updates are
// owned by the daemon coordinator, which drains workers before changing runtime.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, version string) error {
	paths, err := relayruntime.DefaultPaths()
	if err != nil {
		return err
	}
	return run(ctx, args, stdout, stderr, version, paths, os.Environ(), operations{
		inspect: codexmanaged.Inspect, prepare: codexmanaged.Prepare,
		activate: codexmanaged.ActivateContext, reject: codexmanaged.RejectContext,
		probe: codexprofile.RunCapabilityPreflight,
	})
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, version string, paths relayruntime.Paths, env []string, ops operations) error {
	if len(args) == 0 {
		return fmt.Errorf("expected status, prepare or activate")
	}
	command := args[0]
	if command != "status" && command != "prepare" && command != "activate" {
		return fmt.Errorf("unsupported codex-runtime command: %s", command)
	}
	flags := flag.NewFlagSet("codex-runtime "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", filepath.Join(paths.StateDir, "codex-runtime"), "private runtime directory; daemon guard uses this user's XDG_STATE_HOME")
	channel := flags.String("channel", "stable", "official channel: stable or alpha")
	pin := flags.String("version", "", "exact official version; activate defaults to the prepared candidate")
	retry := flags.Bool("retry-failed", false, "explicitly retry a previously failed release during preparation")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if *channel != "stable" && *channel != "alpha" {
		return fmt.Errorf("channel must be stable or alpha")
	}
	if strings.TrimSpace(*root) == "" {
		return fmt.Errorf("private runtime root is empty")
	}
	absoluteRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if command == "status" {
		state, err := ops.inspect(absoluteRoot)
		if err != nil {
			return err
		}
		return writeJSON(stdout, state)
	}
	if command == "prepare" {
		release, err := ops.prepare(ctx, absoluteRoot, codexmanaged.Options{Channel: *channel, Version: *pin, RetryFailed: *retry}, env)
		if err != nil {
			return err
		}
		state, err := ops.inspect(absoluteRoot)
		if err != nil {
			return err
		}
		if state.Active != nil && state.Active.Version == release.Version {
			return writeJSON(stdout, release)
		}
		if state.Candidate == nil || state.Candidate.Version != release.Version {
			return fmt.Errorf("prepared candidate changed before verification; retry preparation")
		}
		if err := probeCandidate(ctx, absoluteRoot, release, env, version, ops); err != nil {
			return err
		}
		return writeJSON(stdout, release)
	}
	if *retry {
		return fmt.Errorf("retry-failed is only valid for prepare")
	}
	lock, err := relayruntime.AcquireLock(ctx, paths.DaemonLockFile, false)
	if err != nil {
		return fmt.Errorf("offline activation requires the daemon to be stopped for this user's XDG_STATE_HOME: %w", err)
	}
	defer lock.Release()
	state, err := ops.inspect(absoluteRoot)
	if err != nil {
		return err
	}
	if state.Candidate == nil {
		return fmt.Errorf("no prepared candidate; run codex-runtime prepare first")
	}
	if *pin != "" && state.Candidate.Version != *pin {
		return fmt.Errorf("prepared candidate %s does not match requested %s", state.Candidate.Version, *pin)
	}
	if err := probeCandidate(ctx, absoluteRoot, *state.Candidate, env, version, ops); err != nil {
		return err
	}
	release, err := ops.activate(ctx, absoluteRoot, state.Candidate.Version)
	if err != nil {
		return err
	}
	return writeJSON(stdout, release)
}

func probeCandidate(ctx context.Context, root string, release codexmanaged.Release, env []string, version string, ops operations) error {
	_, err := ops.probe(ctx, codexprofile.CapabilityPreflightOptions{BinaryPath: release.Binary, Env: env, Version: version})
	if err == nil {
		return nil
	}
	rejectCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if rejectErr := ops.reject(rejectCtx, root, release.Version, err.Error()); rejectErr != nil {
		return fmt.Errorf("candidate capability check failed: %v; record failure: %w", err, rejectErr)
	}
	return fmt.Errorf("candidate %s failed isolated capability check: %w", release.Version, err)
}

func writeJSON(stdout io.Writer, value any) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
