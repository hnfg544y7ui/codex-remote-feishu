//go:build windows

package install

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	relayruntime "github.com/kxn/codex-remote-feishu/internal/runtime"
	"golang.org/x/sys/windows"
)

const windowsUpgradeProbeRole = "CODEX_REMOTE_UPGRADE_TASK_PROBE_ROLE"
const windowsUpgradeProbeDir = "CODEX_REMOTE_UPGRADE_TASK_PROBE_DIR"

var windowsUpgradeProbeArgs = []string{"中文 参数", "apostrophe'$`literal", "quoted\" argument", `trailing\`}

const windowsUpgradeProbeUnicode = "中文 值'$`literal"

func TestWindowsUpgradeHelperTaskSubprocess(t *testing.T) {
	role, dir := os.Getenv(windowsUpgradeProbeRole), os.Getenv(windowsUpgradeProbeDir)
	if role == "" {
		return
	}
	if role == "child" {
		if err := waitForUpgradeHelperLaunch(context.Background()); err != nil {
			t.Fatal(err)
		}
		if journal := os.Getenv("CODEX_REMOTE_UPGRADE_TASK_PROBE_JOURNAL"); journal != "" {
			_ = os.WriteFile(journal, []byte("transaction entered"), 0600)
		}
		workDir, _ := os.Getwd()
		payload, _ := json.Marshal(map[string]any{"PID": os.Getpid(), "Secret": os.Getenv("CODEX_REMOTE_UPGRADE_TASK_PROBE_SECRET"), "WorkDir": workDir, "GateEnv": os.Getenv(upgradeHelperStartGateEnv), "Unicode": os.Getenv("CODEX_REMOTE_UPGRADE_TASK_PROBE_UNICODE"), "Args": flag.Args()})
		_ = os.WriteFile(filepath.Join(dir, "child.json"), payload, 0600)
		fmt.Fprintln(os.Stdout, "upgrade probe stdout")
		fmt.Fprintln(os.Stderr, "upgrade probe stderr")
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(dir, "finish")); err == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		return
	}
	if role != "controller" {
		t.Fatal("unknown isolated probe role")
	}
	executable, _ := os.Executable()
	env := append(os.Environ(), windowsUpgradeProbeRole+"=child", "CODEX_REMOTE_UPGRADE_TASK_PROBE_SECRET=private-probe-value", upgradeHelperStartGateEnv+"=must-be-overridden", "CODEX_REMOTE_UPGRADE_TASK_PROBE_UNICODE="+windowsUpgradeProbeUnicode)
	args := append([]string{"-test.run=^TestWindowsUpgradeHelperTaskSubprocess$", "--"}, windowsUpgradeProbeArgs...)
	result, err := startWindowsUpgradeHelperTask(context.Background(), relayruntime.DetachedCommandOptions{BinaryPath: executable, Args: args, Env: env, WorkDir: dir, StdoutPath: filepath.Join(dir, "stdout.log"), StderrPath: filepath.Join(dir, "stderr.log")})
	payload, _ := json.Marshal(map[string]any{"TaskName": result.UnitName, "PID": os.Getpid(), "Error": fmt.Sprint(err)})
	_ = os.WriteFile(filepath.Join(dir, "controller.json"), payload, 0600)
	time.Sleep(45 * time.Second)
}

// Opt in only: registers unique test tasks, never operates a production task.
func TestWindowsUpgradeHelperIndependentScheduledTask(t *testing.T) {
	if os.Getenv("CODEX_REMOTE_TEST_WINDOWS_UPGRADE_TASK") != "1" {
		t.Skip("set CODEX_REMOTE_TEST_WINDOWS_UPGRADE_TASK=1 for isolated real scheduler regression")
	}
	dir := filepath.Join(t.TempDir(), "中文 空格'$`literal")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	taskName := fmt.Sprintf("CodexRemoteUpgradeProbe-%d", time.Now().UnixNano())
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	launcher := filepath.Join(dir, "old-controller.ps1")
	source := "$env:" + windowsUpgradeProbeRole + "='controller'\n$env:" + windowsUpgradeProbeDir + "=" + quote(dir) + "\n& " + quote(executable) + " '-test.run=^TestWindowsUpgradeHelperTaskSubprocess$'\n"
	if err := os.WriteFile(launcher, append([]byte{0xef, 0xbb, 0xbf}, []byte(source)...), 0600); err != nil {
		t.Fatal(err)
	}
	user, err := serviceCurrentUser()
	if err != nil {
		t.Fatal(err)
	}
	register := "$ErrorActionPreference='Stop'; $a=New-ScheduledTaskAction -Execute 'powershell.exe' -Argument " + quote(windowsCommandLineArgs("-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", launcher)) + "; $p=New-ScheduledTaskPrincipal -UserId " + quote(user) + " -LogonType Interactive -RunLevel Limited; Register-ScheduledTask -TaskName " + quote(taskName) + " -Action $a -Principal $p | Out-Null; Start-ScheduledTask -TaskName " + quote(taskName)
	var controllerPID, childPID int
	var helperName string
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(dir, "finish"), []byte("done"), 0600)
		_, _ = runTaskScheduler(context.Background(), "/End", "/TN", taskName)
		_, _ = runTaskScheduler(context.Background(), "/Delete", "/TN", taskName, "/F")
		if controllerPID > 0 {
			_ = relayruntime.TerminateProcess(controllerPID, time.Second)
		}
		if childPID > 0 {
			_ = relayruntime.TerminateProcess(childPID, time.Second)
		}
		if helperName != "" {
			_, _ = runTaskScheduler(context.Background(), "/Delete", "/TN", helperName, "/F")
		}
	})
	if _, err := runTaskSchedulerPowerShell(context.Background(), register); err != nil {
		t.Fatal(err)
	}
	waitFile := func(name string) []byte {
		t.Helper()
		deadline := time.Now().Add(25 * time.Second)
		for time.Now().Before(deadline) {
			if data, err := os.ReadFile(filepath.Join(dir, name)); err == nil && len(data) > 0 {
				return data
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("missing %s", name)
		return nil
	}
	var controller struct {
		TaskName string
		PID      int
		Error    string
	}
	if err := json.Unmarshal(waitFile("controller.json"), &controller); err != nil {
		t.Fatal(err)
	}
	controllerPID, helperName = controller.PID, controller.TaskName
	if controller.Error != "<nil>" {
		t.Fatalf("controller launch: %s", controller.Error)
	}
	var child struct {
		PID     int
		Secret  string
		WorkDir string
		GateEnv string
		Unicode string
		Args    []string
	}
	if err := json.Unmarshal(waitFile("child.json"), &child); err != nil {
		t.Fatal(err)
	}
	childPID = child.PID
	if child.Secret != "private-probe-value" || child.WorkDir != dir || child.GateEnv != "" || child.Unicode != windowsUpgradeProbeUnicode || !reflect.DeepEqual(child.Args, windowsUpgradeProbeArgs) {
		t.Fatalf("wrong child state %+v", child)
	}
	xml, err := runTaskScheduler(context.Background(), "/Query", "/TN", helperName, "/XML")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(xml, "private-probe-value") || strings.Contains(xml, "PROBE_SECRET") {
		t.Fatal("sensitive environment leaked to task XML")
	}
	artifactDir := filepath.Join(os.TempDir(), helperName)
	aclScript := "$a=[IO.Directory]::GetAccessControl(" + quote(artifactDir) + "); if (!$a.AreAccessRulesProtected -or $a.Access.Count -ne 2) { throw 'private ACL not protected' }; foreach ($r in $a.Access) { $s=$r.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value; if ($s -ne [Security.Principal.WindowsIdentity]::GetCurrent().User.Value -and $s -ne 'S-1-5-18') { throw 'unexpected ACL principal' } }"
	if _, err := runTaskSchedulerPowerShell(context.Background(), aclScript); err != nil {
		t.Fatal(err)
	}
	if _, err := runTaskScheduler(context.Background(), "/End", "/TN", taskName); err != nil {
		t.Fatal(err)
	}
	if err := relayruntime.TerminateProcess(controllerPID, time.Second); err != nil {
		t.Fatal(err)
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(childPID))
	if err != nil {
		t.Fatalf("helper died with old task: %v", err)
	}
	var exitCode uint32
	err = windows.GetExitCodeProcess(handle, &exitCode)
	windows.CloseHandle(handle)
	if err != nil || exitCode != 259 {
		t.Fatalf("helper died exit=%d error=%v", exitCode, err)
	}
	_ = os.WriteFile(filepath.Join(dir, "finish"), []byte("done"), 0600)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(artifactDir); os.IsNotExist(err) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := os.Stat(artifactDir); !os.IsNotExist(err) {
		t.Fatalf("private artifact directory not cleaned: %v", err)
	}
	if _, err := runTaskScheduler(context.Background(), "/Query", "/TN", helperName); err == nil {
		t.Fatal("helper task not cleaned")
	}
	for _, name := range []string{"stdout.log", "stderr.log"} {
		data := waitFile(name)
		if !strings.Contains(string(data), "upgrade probe") {
			t.Fatalf("missing redirected output in %s", name)
		}
	}
}

func TestWindowsUpgradeHelperScheduledTaskStartFailureCleansArtifacts(t *testing.T) {
	if os.Getenv("CODEX_REMOTE_TEST_WINDOWS_UPGRADE_TASK") != "1" {
		t.Skip("set CODEX_REMOTE_TEST_WINDOWS_UPGRADE_TASK=1 for isolated real scheduler regression")
	}
	pattern := filepath.Join(os.TempDir(), "CodexRemoteUpgradeHelper-*")
	before, _ := filepath.Glob(pattern)
	known := map[string]bool{}
	for _, path := range before {
		known[path] = true
	}
	dir := t.TempDir()
	_, err := startWindowsUpgradeHelperTask(context.Background(), relayruntime.DetachedCommandOptions{
		BinaryPath: filepath.Join(dir, "missing-helper.exe"),
		Env:        os.Environ(), WorkDir: dir, StdoutPath: filepath.Join(dir, "helper.log"),
	})
	if err == nil {
		t.Fatal("missing binary unexpectedly launched")
	}
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		after, _ := filepath.Glob(pattern)
		remaining := false
		for _, path := range after {
			if !known[path] {
				remaining = true
			}
		}
		if !remaining {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("failed helper launch left private artifacts")
}

func TestWindowsUpgradeHelperCanceledAcknowledgmentLeavesJournalUnchanged(t *testing.T) {
	if os.Getenv("CODEX_REMOTE_TEST_WINDOWS_UPGRADE_TASK") != "1" {
		t.Skip("set CODEX_REMOTE_TEST_WINDOWS_UPGRADE_TASK=1 for isolated real scheduler regression")
	}
	dir := t.TempDir()
	journal := filepath.Join(dir, "journal.json")
	original := []byte("prepared journal")
	if err := os.WriteFile(journal, original, 0600); err != nil {
		t.Fatal(err)
	}
	executable, _ := os.Executable()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	previous := windowsUpgradeTaskRunner
	var ack windowsUpgradeTaskAck
	windowsUpgradeTaskRunner = func(ctx context.Context, input []byte) (string, error) {
		output, err := runWindowsUpgradeTaskBootstrap(ctx, input)
		if err == nil {
			_ = json.Unmarshal([]byte(output), &ack)
			cancel()
		}
		return output, err
	}
	t.Cleanup(func() { windowsUpgradeTaskRunner = previous })
	env := append(os.Environ(), windowsUpgradeProbeRole+"=child", windowsUpgradeProbeDir+"="+dir, "CODEX_REMOTE_UPGRADE_TASK_PROBE_JOURNAL="+journal)
	_, err := startWindowsUpgradeHelperTask(ctx, relayruntime.DetachedCommandOptions{BinaryPath: executable, Args: []string{"-test.run=^TestWindowsUpgradeHelperTaskSubprocess$"}, Env: env, WorkDir: dir, StdoutPath: filepath.Join(dir, "helper.log")})
	if err != context.Canceled {
		t.Fatalf("expected cancellation before ownership commit: %v", err)
	}
	if ack.TaskName == "" {
		t.Fatal("test did not reach acknowledgment window")
	}
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ack.ArtifactDir); os.IsNotExist(err) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	data, _ := os.ReadFile(journal)
	if string(data) != string(original) {
		t.Fatal("canceled launch entered transactional work")
	}
	if _, err := os.Stat(ack.ArtifactDir); !os.IsNotExist(err) {
		t.Fatal("canceled helper artifacts not cleaned")
	}
	if _, err := runTaskScheduler(context.Background(), "/Query", "/TN", ack.TaskName); err == nil {
		t.Fatal("canceled helper task not cleaned")
	}
}

func TestWindowsUpgradeHelperCancellationAroundRegistrationCleansOwnedTask(t *testing.T) {
	if os.Getenv("CODEX_REMOTE_TEST_WINDOWS_UPGRADE_TASK") != "1" {
		t.Skip("opt-in isolated Task Scheduler regression")
	}
	for _, phase := range []string{"before-register", "after-register"} {
		t.Run(phase, func(t *testing.T) {
			journal := filepath.Join(t.TempDir(), "journal")
			_ = os.WriteFile(journal, []byte("prepared"), 0600)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			previous := windowsUpgradeTaskRunner
			t.Cleanup(func() { windowsUpgradeTaskRunner = previous })
			var owned windowsUpgradeTaskSpec
			windowsUpgradeTaskRunner = func(ctx context.Context, input []byte) (string, error) {
				_ = json.Unmarshal(input, &owned)
				hook := "\n  [IO.File]::WriteAllText((Join-Path $dir 'checkpoint'),'ready')\n  while (![IO.File]::Exists((Join-Path $dir 'cancel'))) { Start-Sleep -Milliseconds 50 }\n"
				needle := "  Register-ScheduledTask -TaskName $name"
				replacement := hook + needle
				if phase == "after-register" {
					needle = "  $registered=$true"
					replacement = needle + hook
				}
				script := strings.Replace(windowsUpgradeTaskBootstrapScript, needle, replacement, 1)
				go func() {
					deadline := time.Now().Add(8 * time.Second)
					for time.Now().Before(deadline) {
						if _, err := os.Stat(filepath.Join(owned.ArtifactDir, "checkpoint")); err == nil {
							cancel()
							return
						}
						time.Sleep(50 * time.Millisecond)
					}
					cancel()
				}()
				return runWindowsUpgradeTaskBootstrapScript(ctx, input, script)
			}
			executable, _ := os.Executable()
			_, err := startWindowsUpgradeHelperTask(ctx, relayruntime.DetachedCommandOptions{BinaryPath: executable, Args: []string{"-test.run=^TestWindowsUpgradeHelperTaskSubprocess$"}, Env: os.Environ()})
			if err == nil {
				t.Fatal("canceled registration reported success")
			}
			data, _ := os.ReadFile(journal)
			if string(data) != "prepared" {
				t.Fatal("cancelled bootstrap mutated journal")
			}
			if _, err := os.Stat(owned.ArtifactDir); !os.IsNotExist(err) {
				t.Fatal("canceled registration left owned artifacts")
			}
			if _, err := runTaskScheduler(context.Background(), "/Query", "/TN", owned.TaskName); err == nil {
				t.Fatal("canceled registration left owned task")
			}
		})
	}
}
