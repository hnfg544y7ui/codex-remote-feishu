//go:build windows

package relayruntime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/execlaunch"
)

func TestWindowsAncestorStopKeepsUpgradeControllerAlive(t *testing.T) {
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := execlaunch.Command(executable, "-test.run=^TestWindowsStopSubprocess$")
	cmd.Env = append(os.Environ(), "REMOTE_STOP_ROLE=parent", "REMOTE_STOP_DIR="+dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = TerminateProcess(cmd.Process.Pid, time.Second)
		_ = cmd.Wait()
	})
	deadline := time.Now().Add(15 * time.Second)
	var proof struct {
		ParentStopped, WorkerStopped, ControllerAlive bool
		Error                                         string
	}
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(filepath.Join(dir, "proof.json"))
		if readErr == nil {
			if err := json.Unmarshal(data, &proof); err != nil {
				t.Fatal(err)
			}
			if proof.Error != "" || !proof.ParentStopped || !proof.WorkerStopped || !proof.ControllerAlive {
				t.Fatalf("stop proof: %+v", proof)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("controller did not survive ancestor shutdown")
}

func TestWindowsStopSubprocess(t *testing.T) {
	role := os.Getenv("REMOTE_STOP_ROLE")
	if role == "" {
		return
	}
	dir := os.Getenv("REMOTE_STOP_DIR")
	executable, err := os.Executable()
	if err != nil {
		os.Exit(2)
	}
	switch role {
	case "worker":
		time.Sleep(20 * time.Second)
	case "parent":
		worker := execlaunch.Command(executable, "-test.run=^TestWindowsStopSubprocess$")
		worker.Env = append(os.Environ(), "REMOTE_STOP_ROLE=worker")
		if err := worker.Start(); err != nil {
			os.Exit(3)
		}
		controller := execlaunch.Command(executable, "-test.run=^TestWindowsStopSubprocess$")
		controller.Env = append(os.Environ(), "REMOTE_STOP_ROLE=controller", "REMOTE_STOP_PARENT="+strconv.Itoa(os.Getpid()), "REMOTE_STOP_WORKER="+strconv.Itoa(worker.Process.Pid))
		if err := controller.Start(); err != nil {
			_ = worker.Process.Kill()
			os.Exit(4)
		}
		time.Sleep(20 * time.Second)
	case "controller":
		parent, _ := strconv.Atoi(os.Getenv("REMOTE_STOP_PARENT"))
		worker, _ := strconv.Atoi(os.Getenv("REMOTE_STOP_WORKER"))
		proof := struct {
			ParentStopped, WorkerStopped, ControllerAlive bool
			Error                                         string
		}{}
		if err := TerminateProcess(parent, 3*time.Second); err != nil {
			proof.Error = err.Error()
		}
		proof.ParentStopped = !processAlive(parent)
		proof.WorkerStopped = !processAlive(worker)
		proof.ControllerAlive = processAlive(os.Getpid())
		data, err := json.Marshal(proof)
		if err != nil {
			os.Exit(5)
		}
		if err := os.WriteFile(filepath.Join(dir, "proof.json"), data, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(6)
		}
	default:
		os.Exit(7)
	}
	os.Exit(0)
}
