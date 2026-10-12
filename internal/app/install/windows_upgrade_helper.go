package install

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/kxn/codex-remote-feishu/internal/execlaunch"
	relayruntime "github.com/kxn/codex-remote-feishu/internal/runtime"
)

type windowsUpgradeTaskSpec struct {
	TaskName     string
	ArtifactDir  string
	BinaryPath   string
	Arguments    string
	Env          []string
	WorkDir      string
	StdoutPath   string
	StderrPath   string
	RunnerScript string
}

type windowsUpgradeTaskAck struct {
	TaskName    string
	PID         int
	ArtifactDir string
}

// Input travels through stdin, never through Task Scheduler XML or process argv.
var windowsUpgradeTaskRunner = runWindowsUpgradeTaskBootstrap
var windowsUpgradeTaskCleanup = cleanupWindowsUpgradeTask

func startWindowsUpgradeHelperTask(ctx context.Context, opts relayruntime.DetachedCommandOptions) (result UpgradeHelperLaunchResult, launchErr error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return result, err
	}
	taskName := "CodexRemoteUpgradeHelper-" + hex.EncodeToString(id[:])
	spec := windowsUpgradeTaskSpec{
		TaskName:     taskName,
		ArtifactDir:  filepath.Join(os.TempDir(), taskName),
		BinaryPath:   opts.BinaryPath,
		Arguments:    windowsCommandLineArgs(opts.Args...),
		Env:          append([]string{}, opts.Env...),
		WorkDir:      opts.WorkDir,
		StdoutPath:   opts.StdoutPath,
		StderrPath:   opts.StderrPath,
		RunnerScript: windowsUpgradeTaskChildScript,
	}
	committed := false
	defer func() {
		if !committed {
			if err := windowsUpgradeTaskCleanup(spec); err != nil {
				launchErr = fmt.Errorf("%w; scoped startup cleanup: %v", launchErr, err)
			}
		}
	}()
	input, err := json.Marshal(spec)
	if err != nil {
		return UpgradeHelperLaunchResult{}, err
	}
	launchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	output, err := windowsUpgradeTaskRunner(launchCtx, input)
	if err != nil {
		// Bootstrap errors deliberately contain no launch JSON / environment.
		return UpgradeHelperLaunchResult{}, fmt.Errorf("launch independent Windows upgrade task: %w", err)
	}
	var ack windowsUpgradeTaskAck
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &ack); err != nil {
		return UpgradeHelperLaunchResult{}, fmt.Errorf("decode Windows upgrade task acknowledgment: %w", err)
	}
	if ack.TaskName != spec.TaskName || ack.ArtifactDir != spec.ArtifactDir {
		return result, fmt.Errorf("Windows helper acknowledgment did not match its owned launch identity")
	}
	if err := commitWindowsUpgradeTask(launchCtx, ack); err != nil {
		return UpgradeHelperLaunchResult{}, err
	}
	committed = true
	return UpgradeHelperLaunchResult{UnitName: ack.TaskName}, nil
}

func commitWindowsUpgradeTask(ctx context.Context, ack windowsUpgradeTaskAck) error {
	const prefix = "CodexRemoteUpgradeHelper-"
	id := strings.TrimPrefix(ack.TaskName, prefix)
	if !strings.HasPrefix(ack.TaskName, prefix) || len(id) != 32 || ack.PID <= 0 {
		return fmt.Errorf("Windows upgrade task did not acknowledge a started helper")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return fmt.Errorf("invalid Windows upgrade helper task identifier")
	}
	expected := filepath.Join(os.TempDir(), ack.TaskName)
	if ack.ArtifactDir != expected {
		return fmt.Errorf("invalid Windows upgrade helper artifact directory")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.CreateTemp(expected, ".receipt-*")
	if err != nil {
		return fmt.Errorf("prepare upgrade ownership receipt: %w", err)
	}
	defer os.Remove(file.Name())
	if _, err = file.WriteString("committed"); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// Rename is the ownership transfer. Once committed, cancellation may not
	// report failure and cause the controller to restore an older journal.
	if err = os.Rename(file.Name(), filepath.Join(expected, "receipt")); err != nil {
		return fmt.Errorf("commit upgrade ownership receipt: %w", err)
	}
	return nil
}

func runWindowsUpgradeTaskBootstrap(ctx context.Context, input []byte) (string, error) {
	return runWindowsUpgradeTaskBootstrapScript(ctx, input, windowsUpgradeTaskBootstrapScript)
}

func runWindowsUpgradeTaskBootstrapScript(ctx context.Context, input []byte, script string) (string, error) {
	cmd := execlaunch.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", encodeWindowsUpgradeScript(script))
	cmd.Stdin = strings.NewReader(string(input))
	var spec windowsUpgradeTaskSpec
	if err := json.Unmarshal(input, &spec); err != nil {
		return "", err
	}
	// Let registration finish and undo itself on cancellation instead of killing
	// the bootstrap during a Task Scheduler RPC. WaitDelay bounds a stuck host.
	cmd.Cancel = func() error {
		_ = os.WriteFile(filepath.Join(spec.ArtifactDir, "cancel"), []byte("cancel"), 0600)
		return nil
	}
	cmd.WaitDelay = 25 * time.Second
	var output, diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &diagnostic
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(decodeTaskSchedulerOutput(diagnostic.Bytes())))
	}
	return strings.TrimSpace(decodeTaskSchedulerOutput(output.Bytes())), nil
}

func cleanupWindowsUpgradeTask(spec windowsUpgradeTaskSpec) error {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	input, err := json.Marshal(map[string]string{"TaskName": spec.TaskName, "ArtifactDir": spec.ArtifactDir})
	if err != nil {
		return err
	}
	cmd := execlaunch.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", encodeWindowsUpgradeScript(windowsUpgradeTaskCleanupScript))
	cmd.Stdin = strings.NewReader(string(input))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(decodeTaskSchedulerOutput(output)))
	}
	return nil
}

func encodeWindowsUpgradeScript(script string) string {
	units := utf16.Encode([]rune(script))
	bytes := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(bytes[i*2:], unit)
	}
	return base64.StdEncoding.EncodeToString(bytes)
}
