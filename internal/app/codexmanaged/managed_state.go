package codexmanaged

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Options selects an official channel, or an exact version overriding the channel.
// RetryFailed is an explicit operator choice; automatic updates must leave it false.
type Options struct {
	Channel     string `json:"channel,omitempty"`
	Version     string `json:"version,omitempty"`
	RetryFailed bool   `json:"-"`
}

// Release describes an immutable private installation. Components map relative
// executable paths to SHA-256 digests; Binary is always a native executable.
type Release struct {
	Version    string            `json:"version"`
	Binary     string            `json:"binary"`
	Components map[string]string `json:"components"`
	Channel    string            `json:"channel"`
	PreparedAt time.Time         `json:"preparedAt"`
}

type Failure struct {
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// State is the only selection authority. Directory ordering never selects a CLI.
type State struct {
	SchemaVersion int                `json:"schemaVersion"`
	Active        *Release           `json:"active,omitempty"`
	Previous      *Release           `json:"previous,omitempty"`
	Candidate     *Release           `json:"candidate,omitempty"`
	Failed        map[string]Failure `json:"failed,omitempty"`
	Options       Options            `json:"options"`
	UpdatedAt     time.Time          `json:"updatedAt"`
}

func managedRoot(root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("managed Codex root is empty")
	}
	return filepath.Abs(root)
}

// Inspect reports persisted selection without reaching npm or launching a CLI.
func Inspect(root string) (State, error) {
	root, err := managedRoot(root)
	if err != nil {
		return State{}, err
	}
	return readManagedState(root)
}

func readManagedState(root string) (State, error) {
	// Only serialize tiny metadata reads/writes, independently of the download
	// lock. Windows cannot always rename a file while a reader has it open.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lock, err := lockManagedFile(ctx, root, "managed-state.lock")
	if err != nil {
		return State{}, err
	}
	defer lock.release()
	state := State{SchemaVersion: 1, Failed: map[string]Failure{}}
	raw, err := readManagedJSONFile(filepath.Join(root, "managed.json"))
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return State{}, err
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, fmt.Errorf("read managed Codex state: %w", err)
	}
	if state.SchemaVersion != 1 {
		return State{}, fmt.Errorf("unsupported managed Codex state schema %d", state.SchemaVersion)
	}
	if state.Failed == nil {
		state.Failed = map[string]Failure{}
	}
	return state, nil
}

func writeManagedState(root string, state State) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lock, err := lockManagedFile(ctx, root, "managed-state.lock")
	if err != nil {
		return err
	}
	defer lock.release()
	state.SchemaVersion, state.UpdatedAt = 1, time.Now().UTC()
	state.Options.RetryFailed = false
	return writeManagedJSON(filepath.Join(root, "managed.json"), state)
}

func writeManagedJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".managed-json-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return renameManagedJSON(file.Name(), path)
}
