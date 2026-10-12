package install

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	relayruntime "github.com/kxn/codex-remote-feishu/internal/runtime"
)

// Existing lifecycle fixtures inspect detached command options; intercept the
// Windows boundary too so ordinary unit tests never register real tasks.
func stubWindowsUpgradeTaskFromDetached(t *testing.T) {
	t.Helper()
	previous := upgradeHelperStartWindowsTaskFunc
	upgradeHelperStartWindowsTaskFunc = func(_ context.Context, opts relayruntime.DetachedCommandOptions) (UpgradeHelperLaunchResult, error) {
		_, err := upgradeHelperStartDetachedCommandFunc(opts)
		return UpgradeHelperLaunchResult{}, err
	}
	t.Cleanup(func() { upgradeHelperStartWindowsTaskFunc = previous })
}

func TestWindowsUpgradeHelperDispatchIsIndependentAndFailClosed(t *testing.T) {
	oldGOOS, oldWindows, oldDetached := serviceRuntimeGOOS, upgradeHelperStartWindowsTaskFunc, upgradeHelperStartDetachedCommandFunc
	t.Cleanup(func() {
		serviceRuntimeGOOS, upgradeHelperStartWindowsTaskFunc, upgradeHelperStartDetachedCommandFunc = oldGOOS, oldWindows, oldDetached
	})
	serviceRuntimeGOOS = "windows"
	upgradeHelperStartDetachedCommandFunc = func(relayruntime.DetachedCommandOptions) (int, error) {
		t.Fatal("unsafe detached fallback")
		return 0, nil
	}
	for _, manager := range []ServiceManager{ServiceManagerTaskSchedulerLogon, ServiceManagerDetached} {
		for _, direct := range []bool{false, true} {
			var captured relayruntime.DetachedCommandOptions
			upgradeHelperStartWindowsTaskFunc = func(_ context.Context, opts relayruntime.DetachedCommandOptions) (UpgradeHelperLaunchResult, error) {
				captured = opts
				return UpgradeHelperLaunchResult{UnitName: "test-task"}, nil
			}
			result, err := StartUpgradeHelperProcess(context.Background(), UpgradeHelperLaunchOptions{State: InstallState{ServiceManager: manager}, HelperBinary: "helper", StatePath: "state", LogPath: "log", Env: []string{"SECRET=value"}, WorkDir: "work", DirectExec: direct})
			if err != nil || result.UnitName != "test-task" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if captured.BinaryPath != "helper" || captured.WorkDir != "work" || captured.StdoutPath != "log" || captured.StderrPath != "log" || !reflect.DeepEqual(captured.Env, []string{"SECRET=value"}) {
				t.Fatalf("incorrect transport: %+v", captured)
			}
			if direct && len(captured.Args) != 0 || !direct && !reflect.DeepEqual(captured.Args, []string{"upgrade-helper", "-state-path", "state"}) {
				t.Fatalf("args=%v direct=%v", captured.Args, direct)
			}
		}
	}
	blocked := errors.New("scheduler unavailable")
	upgradeHelperStartWindowsTaskFunc = func(context.Context, relayruntime.DetachedCommandOptions) (UpgradeHelperLaunchResult, error) {
		return UpgradeHelperLaunchResult{}, blocked
	}
	_, err := StartUpgradeHelperProcess(context.Background(), UpgradeHelperLaunchOptions{HelperBinary: "helper", StatePath: "state"})
	if !errors.Is(err, blocked) {
		t.Fatalf("err=%v", err)
	}
}

func TestWindowsUpgradeTaskTransportsExactSpecOnlyOnStdin(t *testing.T) {
	previous, previousCleanup := windowsUpgradeTaskRunner, windowsUpgradeTaskCleanup
	t.Cleanup(func() { windowsUpgradeTaskRunner = previous; windowsUpgradeTaskCleanup = previousCleanup })
	windowsUpgradeTaskCleanup = func(spec windowsUpgradeTaskSpec) error { return os.RemoveAll(spec.ArtifactDir) }
	var taskName, artifactDir string
	opts := relayruntime.DetachedCommandOptions{BinaryPath: `C:\path with spaces\helper.exe`, Args: []string{"quotes\"", `C:\ends\`, ""}, Env: []string{"SECRET=sensitive", "PATH=x=y"}, WorkDir: `C:\working dir`, StdoutPath: "stdout", StderrPath: "stderr"}
	fake := func(ctx context.Context, input []byte) (string, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing bounded startup")
		}
		var spec windowsUpgradeTaskSpec
		if err := json.Unmarshal(input, &spec); err != nil {
			t.Fatal(err)
		}
		taskName, artifactDir = spec.TaskName, spec.ArtifactDir
		if artifactDir != filepath.Join(os.TempDir(), taskName) {
			t.Fatal("incorrect owned artifact identity")
		}
		if err := os.Mkdir(artifactDir, 0700); err != nil {
			t.Fatal(err)
		}
		owned := artifactDir
		t.Cleanup(func() { _ = os.RemoveAll(owned) })
		if !reflect.DeepEqual(spec.Env, opts.Env) || spec.Arguments != windowsCommandLineArgs(opts.Args...) || spec.BinaryPath != opts.BinaryPath || spec.WorkDir != opts.WorkDir || spec.StdoutPath != opts.StdoutPath || spec.StderrPath != opts.StderrPath {
			t.Fatalf("spec mismatch %+v", spec)
		}
		if strings.Contains(windowsUpgradeTaskBootstrapScript, "sensitive") {
			t.Fatal("secret in CLI bootstrap")
		}
		ack, _ := json.Marshal(windowsUpgradeTaskAck{TaskName: taskName, PID: 123, ArtifactDir: artifactDir})
		return string(ack), nil
	}
	windowsUpgradeTaskRunner = fake
	result, err := startWindowsUpgradeHelperTask(context.Background(), opts)
	if err != nil || result.UnitName != taskName {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if data, err := os.ReadFile(filepath.Join(artifactDir, "receipt")); err != nil || string(data) != "committed" {
		t.Fatalf("receipt=%q err=%v", data, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := startWindowsUpgradeHelperTask(canceled, opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("launch cancellation=%v", err)
	}
	if _, err := os.Stat(filepath.Join(artifactDir, "receipt")); !os.IsNotExist(err) {
		t.Fatal("canceled launch committed")
	}
	for _, invalid := range []string{`{}`, `{"TaskName":"CodexRemoteUpgradeHelper-x","PID":0}`, `{"TaskName":"stable","PID":1}`, `not-json`} {
		windowsUpgradeTaskRunner = func(context.Context, []byte) (string, error) { return invalid, nil }
		if _, err := startWindowsUpgradeHelperTask(context.Background(), opts); err == nil {
			t.Fatalf("accepted invalid ack %q", invalid)
		}
	}
	for _, invalid := range []windowsUpgradeTaskAck{
		{TaskName: taskName, PID: 1, ArtifactDir: filepath.Join(os.TempDir(), "different-folder")},
		{TaskName: "CodexRemoteUpgradeHelper-" + strings.Repeat("g", 32), PID: 1, ArtifactDir: artifactDir},
		{TaskName: "CodexRemoteUpgradeHelper-../stable", PID: 1, ArtifactDir: artifactDir},
		{TaskName: taskName, PID: 0, ArtifactDir: artifactDir},
	} {
		if err := commitWindowsUpgradeTask(context.Background(), invalid); err == nil {
			t.Fatalf("accepted unsafe ack %+v", invalid)
		}
	}
}
