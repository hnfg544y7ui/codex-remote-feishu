package codexmanaged

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/execlaunch"
)

func TestManagedSubprocessBootstrapWorker(t *testing.T) {
	root := os.Getenv("CODEX_MANAGED_TEST_ROOT")
	if root == "" {
		t.Skip("subprocess worker")
	}
	ops := fakeManagedOperations("0.162.1")
	install := ops.install
	ops.install = func(ctx context.Context, prefix, target string) error {
		file, err := os.OpenFile(filepath.Join(root, "install-count"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := file.WriteString("installed\n"); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		// Hold the lock long enough for other independent processes to contend.
		time.Sleep(100 * time.Millisecond)
		return install(ctx, prefix, target)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := ensureManaged(ctx, root, Options{}, ops); err != nil {
		t.Fatal(err)
	}
}

func TestManagedBootstrapSerializesAcrossProcesses(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	type running struct {
		wait   func() error
		output *bytes.Buffer
	}
	var children []running
	for i := 0; i < 6; i++ {
		cmd := execlaunch.CommandContext(ctx, os.Args[0], "-test.run=^TestManagedSubprocessBootstrapWorker$")
		cmd.Env = append(os.Environ(), "CODEX_MANAGED_TEST_ROOT="+root)
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		children = append(children, running{wait: cmd.Wait, output: &output})
	}
	for _, child := range children {
		if err := child.wait(); err != nil {
			t.Fatalf("worker=%v %s", err, child.output.String())
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, "install-count"))
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(raw), "installed\n"); count != 1 {
		t.Fatalf("cross-process installation count=%d", count)
	}
	state, err := Inspect(root)
	if err != nil || state.Active == nil || state.Active.Version != "0.162.1" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestManagedMutationContextsCancelWhileInstallerHoldsLock(t *testing.T) {
	root := t.TempDir()
	lock, err := lockManaged(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()
	for _, operation := range []func(context.Context) error{
		func(ctx context.Context) error { _, err := ActivateContext(ctx, root, "0.162.1"); return err },
		func(ctx context.Context) error { _, err := RollbackContext(ctx, root); return err },
		func(ctx context.Context) error { return RejectContext(ctx, root, "0.162.1", "invalid protocol") },
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		err := operation(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("mutation ignored caller deadline: %v", err)
		}
	}
}
