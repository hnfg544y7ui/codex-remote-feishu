package codexmanaged

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type managedReleaseValidationError struct{ error }

// Prepare downloads and validates a candidate without changing the active CLI.
// A caller must test the app-server protocol before explicitly activating it.
func Prepare(ctx context.Context, root string, opts Options, env []string) (Release, error) {
	return prepareManaged(ctx, root, opts, managedOperationsWithEnv(env))
}

func prepareManaged(ctx context.Context, root string, opts Options, ops managedOperations) (Release, error) {
	root, err := managedRoot(root)
	if err != nil {
		return Release{}, err
	}
	lock, err := lockManaged(ctx, root)
	if err != nil {
		return Release{}, err
	}
	defer lock.release()
	state, err := readManagedState(root)
	if err != nil {
		return Release{}, err
	}
	return prepareManagedLocked(ctx, root, opts, ops, &state)
}

func prepareManagedLocked(ctx context.Context, root string, opts Options, ops managedOperations, state *State) (Release, error) {
	opts, err := normalizeManagedOptions(opts)
	if err != nil {
		return Release{}, err
	}
	target, err := latestManaged(ctx, opts, ops)
	if err != nil {
		return Release{}, fmt.Errorf("check official managed Codex version: %w", err)
	}
	if failure, failed := state.Failed[target]; failed && !opts.RetryFailed {
		return Release{}, fmt.Errorf("managed Codex %s was rejected: %s; explicit retry required", target, failure.Reason)
	}
	release, err := prepareManagedFiles(ctx, root, target, opts.Channel, ops)
	if err != nil {
		var validation managedReleaseValidationError
		if errors.As(err, &validation) && ctx.Err() == nil {
			state.Failed[target] = Failure{Reason: err.Error(), At: time.Now().UTC()}
			if state.Candidate != nil && state.Candidate.Version == target {
				state.Candidate = nil
			}
			if persistErr := writeManagedState(root, *state); persistErr != nil {
				return Release{}, errors.Join(err, persistErr)
			}
		}
		return Release{}, err
	}
	// Preparing the current version is idempotent, and cannot create a second
	// candidate pointing at an already-active installation.
	if state.Active == nil || state.Active.Version != release.Version {
		state.Candidate = &release
	}
	if opts.RetryFailed {
		delete(state.Failed, target)
	}
	state.Options = opts
	if err := writeManagedState(root, *state); err != nil {
		return Release{}, err
	}
	return release, nil
}

func prepareManagedFiles(ctx context.Context, root, target, channel string, ops managedOperations) (Release, error) {
	directory, err := releaseDirectory(root, target)
	if err != nil {
		return Release{}, err
	}
	if _, err := os.Stat(directory); err == nil {
		release, err := readReleaseManifest(root, target)
		if os.IsNotExist(err) {
			release, err := adoptManagedRelease(ctx, directory, target, channel, ops)
			if err != nil {
				return Release{}, managedReleaseValidationError{err}
			}
			return release, nil
		}
		if err != nil {
			return Release{}, managedReleaseValidationError{fmt.Errorf("existing immutable managed release cannot be overwritten: %w", err)}
		}
		if err := validateManagedFiles(ctx, root, release, true); err != nil {
			return Release{}, managedReleaseValidationError{err}
		}
		validated, err := validateManagedRelease(ctx, root, release, ops)
		if err != nil {
			return Release{}, managedReleaseValidationError{err}
		}
		return validated, nil
	} else if !os.IsNotExist(err) {
		return Release{}, err
	}
	if err := os.MkdirAll(filepath.Dir(directory), 0o700); err != nil {
		return Release{}, err
	}
	staging, err := os.MkdirTemp(root, ".managed-install-")
	if err != nil {
		return Release{}, err
	}
	defer os.RemoveAll(staging)
	if err := ops.install(ctx, staging, target); err != nil {
		return Release{}, fmt.Errorf("install managed Codex %s: %w", target, err)
	}
	release, err := buildManagedRelease(ctx, staging, target, channel, ops)
	if err != nil {
		return Release{}, managedReleaseValidationError{fmt.Errorf("validate managed Codex %s: %w", target, err)}
	}
	relative, err := filepath.Rel(staging, release.Binary)
	if err != nil {
		return Release{}, err
	}
	release.Binary = filepath.Join(directory, relative)
	if err := writeManagedJSON(filepath.Join(staging, "remote-release.json"), release); err != nil {
		return Release{}, err
	}
	if err := os.Rename(staging, directory); err != nil {
		return Release{}, fmt.Errorf("publish immutable managed Codex release: %w", err)
	}
	return release, nil
}
