package codexmanaged

import (
	"context"
	"fmt"
)

// Resolve uses only the explicitly active installation. It works offline and
// never depends on an App binary, npm, or a configured user CLI.
func Resolve(ctx context.Context, root string, env []string) (Release, error) {
	return resolveManaged(ctx, root, managedOperationsWithEnv(env))
}

func resolveManaged(ctx context.Context, root string, ops managedOperations) (Release, error) {
	root, err := managedRoot(root)
	if err != nil {
		return Release{}, err
	}
	state, err := readManagedState(root)
	if err != nil {
		return Release{}, err
	}
	if state.Active == nil {
		return Release{}, fmt.Errorf("no active managed Codex installation; prepare and activate a release first")
	}
	if _, rejected := state.Failed[state.Active.Version]; rejected {
		return Release{}, fmt.Errorf("active managed Codex %s is marked failed", state.Active.Version)
	}
	return validateManagedRelease(ctx, root, *state.Active, ops)
}

// EnsureManaged bootstraps when no active release exists. Once active, it only
// resolves that release; callers explicitly Prepare/probe/Activate updates.
func EnsureManaged(ctx context.Context, root string, opts Options, env []string) (Release, error) {
	ops := managedOperationsWithEnv(env)
	return ensureManaged(ctx, root, opts, ops)
}

func ensureManaged(ctx context.Context, root string, opts Options, ops managedOperations) (Release, error) {
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
	if state.Active != nil {
		if _, rejected := state.Failed[state.Active.Version]; rejected {
			return Release{}, fmt.Errorf("active managed Codex %s is marked failed", state.Active.Version)
		}
		return validateManagedRelease(ctx, root, *state.Active, ops)
	}
	release, err := prepareManagedLocked(ctx, root, opts, ops, &state)
	if err != nil {
		return Release{}, err
	}
	state.Active, state.Candidate = &release, nil
	if err := writeManagedState(root, state); err != nil {
		return Release{}, err
	}
	return release, nil
}
