package codexmanaged

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedAdoptsOnlyExplicitCompleteLegacyPackage(t *testing.T) {
	root := t.TempDir()
	ops := fakeManagedOperations("0.162.1")
	directory := filepath.Join(root, "releases", "0.162.1")
	if err := ops.install(context.Background(), directory, "0.162.1"); err != nil {
		t.Fatal(err)
	}
	// Higher directory presence must not become the selection authority.
	if err := ops.install(context.Background(), filepath.Join(root, "releases", "0.999.0"), "0.999.0"); err != nil {
		t.Fatal(err)
	}
	metadata := filepath.Join(directory, "node_modules", "@openai", "codex", "package.json")
	if err := os.MkdirAll(filepath.Dir(metadata), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadata, []byte(`{"name":"@openai/codex","version":"0.162.1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	binary, err := installedBinary(directory)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	ops.install = func(context.Context, string, string) error {
		t.Fatal("adoption invokes npm")
		return errors.New("offline")
	}
	release, err := prepareManaged(context.Background(), root, Options{}, ops)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(binary)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("legacy binary was replaced: %v", err)
	}
	state, _ := Inspect(root)
	if state.Active != nil || state.Candidate.Version != "0.162.1" || release.Version != "0.162.1" {
		t.Fatalf("adoption activated/scanned directories: %+v", state)
	}
	if _, err := Activate(root, release.Version); err != nil {
		t.Fatal(err)
	}
}

func TestManagedValidationFailureIsRejectedButNetworkFailureCanRetry(t *testing.T) {
	root := t.TempDir()
	ops := fakeManagedOperations("0.162.1")
	complete := ops.install
	ops.install = func(context.Context, string, string) error { return errors.New("registry unavailable") }
	if _, err := prepareManaged(context.Background(), root, Options{}, ops); err == nil {
		t.Fatal("network failure ignored")
	}
	state, _ := Inspect(root)
	if len(state.Failed) != 0 {
		t.Fatal("network failure blacklisted version")
	}
	ops.install = func(_ context.Context, prefix, target string) error { return writeFakeInstall(prefix, target) }
	if _, err := prepareManaged(context.Background(), root, Options{}, ops); err == nil {
		t.Fatal("missing component accepted")
	}
	state, _ = Inspect(root)
	if state.Failed["0.162.1"].Reason == "" {
		t.Fatal("validation failure not persisted")
	}
	ops.install = complete
	if _, err := prepareManaged(context.Background(), root, Options{}, ops); err == nil || !strings.Contains(err.Error(), "explicit retry") {
		t.Fatalf("bad release automatically retried: %v", err)
	}
	if _, err := prepareManaged(context.Background(), root, Options{RetryFailed: true}, ops); err != nil {
		t.Fatal(err)
	}
}

func TestManagedInspectNeverReadsPartialJSONDuringActivation(t *testing.T) {
	root := t.TempDir()
	ops := fakeManagedOperations("0.162.1")
	release, err := prepareManaged(context.Background(), root, Options{}, ops)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				result <- nil
				return
			default:
			}
			state, err := Inspect(root)
			if err != nil {
				result <- err
				return
			}
			if _, err := json.Marshal(state); err != nil {
				result <- err
				return
			}
		}
	}()
	for i := 0; i < 20; i++ {
		if _, err := Activate(root, release.Version); err != nil {
			close(stop)
			<-result
			t.Fatal(err)
		}
	}
	close(stop)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}
