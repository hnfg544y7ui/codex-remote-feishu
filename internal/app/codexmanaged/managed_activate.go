package codexmanaged

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Activate only changes the persisted selection. The caller owns draining old
// workers and probing the candidate, and must not leave multiple active writers.
func Activate(root, target string) (Release, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	return ActivateContext(ctx, root, target)
}

// ActivateContext obeys the caller's deadline while locking and verifying files.
func ActivateContext(ctx context.Context, root, target string) (Release, error) {
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
	if _, failed := state.Failed[target]; failed {
		return Release{}, fmt.Errorf("cannot activate rejected Codex %s", target)
	}
	release, err := readReleaseManifest(root, target)
	if err != nil {
		return Release{}, err
	}
	if err := validateManagedFiles(ctx, root, release, true); err != nil {
		return Release{}, err
	}
	if state.Active == nil || state.Active.Version != target {
		state.Previous = state.Active
	}
	state.Active = &release
	if state.Candidate != nil && state.Candidate.Version == target {
		state.Candidate = nil
	}
	if err := writeManagedState(root, state); err != nil {
		return Release{}, err
	}
	return release, nil
}

// Rollback restores the prior selection and rejects the replaced active version.
// It does not automatically select a directory or restart a worker.
func Rollback(root string) (Release, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	return RollbackContext(ctx, root)
}

// RollbackContext restores the previous release with a bounded lock and checksum check.
func RollbackContext(ctx context.Context, root string) (Release, error) {
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
	if state.Previous == nil {
		return Release{}, fmt.Errorf("no previous managed Codex release to roll back to")
	}
	release := *state.Previous
	if _, failed := state.Failed[release.Version]; failed {
		return Release{}, fmt.Errorf("previous Codex release is rejected")
	}
	if err := validateManagedFiles(ctx, root, release, true); err != nil {
		return Release{}, err
	}
	if state.Active != nil && state.Active.Version != release.Version {
		state.Failed[state.Active.Version] = Failure{Reason: "rolled back by operator", At: time.Now().UTC()}
	}
	state.Active, state.Previous, state.Candidate = &release, nil, nil
	if err := writeManagedState(root, state); err != nil {
		return Release{}, err
	}
	return release, nil
}

// Reject prevents automatic preparation and activation of a failed candidate.
// Rejecting an active release does not silently switch workers to another CLI.
func Reject(root, target, reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return RejectContext(ctx, root, target, reason)
}

// RejectContext obeys the caller's deadline while persisting the rejection.
func RejectContext(ctx context.Context, root, target, reason string) error {
	if _, err := parseVersion(target); err != nil {
		return err
	}
	root, err := managedRoot(root)
	if err != nil {
		return err
	}
	lock, err := lockManaged(ctx, root)
	if err != nil {
		return err
	}
	defer lock.release()
	state, err := readManagedState(root)
	if err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		reason = "candidate verification failed"
	}
	state.Failed[target] = Failure{Reason: reason, At: time.Now().UTC()}
	if state.Candidate != nil && state.Candidate.Version == target {
		state.Candidate = nil
	}
	return writeManagedState(root, state)
}
