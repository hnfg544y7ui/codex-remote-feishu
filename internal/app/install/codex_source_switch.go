package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/atomicfile"
	"github.com/kxn/codex-remote-feishu/internal/config"
	relayruntime "github.com/kxn/codex-remote-feishu/internal/runtime"
)

// CodexSourceSwitch changes executable ownership without changing the Remote release.
type CodexSourceSwitch struct {
	TargetSource string `json:"targetSource"`
	ConfigDigest string `json:"configDigest"`
}

type CodexSourceSwitchOptions struct {
	StatePath, TargetSource, ConfigDigest                             string
	GatewayID, SurfaceSessionID, ChatID, ActorUserID, SourceMessageID string
}

func CodexSourceConfigDigest(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return codexSourceDigest(raw), nil
}

func codexSourceDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateCodexSourceSwitch(change *CodexSourceSwitch) error {
	if change.TargetSource != config.CodexRuntimeSourceManaged && change.TargetSource != config.CodexRuntimeSourceExternal {
		return fmt.Errorf("Codex source switch target must be managed or external")
	}
	if !strings.HasPrefix(change.ConfigDigest, "sha256:") || len(change.ConfigDigest) != 71 {
		return fmt.Errorf("Codex source switch requires a SHA-256 config digest")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(change.ConfigDigest, "sha256:")); err != nil {
		return fmt.Errorf("invalid Codex source config digest: %w", err)
	}
	return nil
}

func validateCodexSourceSwitchConfig(state InstallState) error {
	if state.PendingUpgrade == nil || state.PendingUpgrade.CodexSourceSwitch == nil {
		return nil
	}
	change := state.PendingUpgrade.CodexSourceSwitch
	if err := validateCodexSourceSwitch(change); err != nil {
		return err
	}
	digest, err := CodexSourceConfigDigest(state.ConfigPath)
	if err != nil {
		return err
	}
	if digest != change.ConfigDigest {
		return fmt.Errorf("Codex source configuration changed; reopen the source selector")
	}
	return nil
}

// RunCodexSourceSwitchWithStatePath starts the native upgrade-helper lifecycle.
// The caller must fence dispatch, check all workers are idle (the whole service
// restarts), reject
// environment overrides, and preflight the requested executable before calling.
// Config is patched only after the helper has stopped the daemon; health failure
// restores the existing rollback snapshot without touching release metadata.
func RunCodexSourceSwitchWithStatePath(opts CodexSourceSwitchOptions) error {
	state, err := LoadState(strings.TrimSpace(opts.StatePath))
	if err != nil {
		return err
	}
	if localPendingUpgradeBusy(state.PendingUpgrade) {
		return fmt.Errorf("pending upgrade phase %q is already in progress", state.PendingUpgrade.Phase)
	}
	if strings.TrimSpace(state.CurrentBinaryPath) == "" {
		return fmt.Errorf("current binary path is missing from install state")
	}
	original := state
	now := time.Now().UTC()
	change := &CodexSourceSwitch{TargetSource: strings.TrimSpace(opts.TargetSource), ConfigDigest: strings.TrimSpace(opts.ConfigDigest)}
	state.PendingUpgrade = &PendingUpgrade{
		Phase: PendingUpgradePhasePrepared, Source: UpgradeSourceLocal,
		TargetTrack: state.CurrentTrack, TargetVersion: state.CurrentVersion,
		TargetSlot: state.CurrentSlot, TargetBinaryPath: state.CurrentBinaryPath,
		CodexSourceSwitch: change, RequestedAt: &now,
		GatewayID: opts.GatewayID, SurfaceSessionID: opts.SurfaceSessionID,
		ChatID: opts.ChatID, ActorUserID: opts.ActorUserID, SourceMessageID: opts.SourceMessageID,
	}
	if err := validateCodexSourceSwitchConfig(state); err != nil {
		return err
	}
	updateLock, err := acquireCodexSourceRuntimeLock(context.Background(), state, false)
	if err != nil {
		return err
	}
	defer updateLock.Release()
	backup, err := PrepareRollbackCandidate(state, fmt.Sprintf("codex-source-%d", now.UnixNano()))
	if err != nil {
		return err
	}
	state.RollbackCandidate = backup
	// Recheck after snapshotting so a concurrent edit cannot become an accepted target.
	if err := validateCodexSourceSwitchConfig(state); err != nil {
		return err
	}
	if err := WriteState(opts.StatePath, state); err != nil {
		return err
	}
	helperPath, err := prepareUpgradeHelperShimFunc(opts.StatePath, state.InstanceID)
	if err != nil {
		_ = WriteState(opts.StatePath, original)
		return err
	}
	launchCtx, cancelLaunch := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancelLaunch()
	_, err = StartUpgradeHelperProcess(launchCtx, UpgradeHelperLaunchOptions{
		State: state, HelperBinary: helperPath, StatePath: opts.StatePath,
		LogPath: localUpgradeLogPath(state),
		Env:     append(append([]string{}, os.Environ()...), RuntimeEnvForState(state)...), DirectExec: true,
	})
	if err != nil {
		_ = WriteState(opts.StatePath, original)
		return err
	}
	// Do not write the prepared journal after launch: the detached helper now
	// owns it and may already have advanced to switching/observing/committed.
	return nil
}

func acquireCodexSourceRuntimeLock(ctx context.Context, state InstallState, wait bool) (*relayruntime.FileLock, error) {
	paths := RuntimePathsForState(state)
	lock, err := relayruntime.AcquireLock(ctx, filepath.Join(paths.StateDir, "codex-runtime", "update.lock"), wait)
	if err != nil {
		return nil, fmt.Errorf("lock Codex runtime during source switch: %w", err)
	}
	return lock, nil
}

// Early failures leave the running daemon intact. Publish a terminal journal for
// its watcher without replacing newer transaction/layout metadata with a stale
// snapshot that the helper read before acquiring the runtime update lock.
func failCodexSourceHelperBeforeStop(statePath string, expected InstallState, cause error) error {
	if expected.PendingUpgrade == nil || expected.PendingUpgrade.CodexSourceSwitch == nil {
		return cause
	}
	latest, err := LoadState(statePath)
	if err != nil {
		return fmt.Errorf("%w; read source-switch failure journal: %v", cause, err)
	}
	if !samePreparedCodexSourceSwitch(expected.PendingUpgrade, latest.PendingUpgrade) {
		return cause
	}
	latest.PendingUpgrade.Phase = PendingUpgradePhaseFailed
	if err := WriteState(statePath, latest); err != nil {
		return fmt.Errorf("%w; record source-switch failure: %v", cause, err)
	}
	return cause
}

func samePreparedCodexSourceSwitch(expected, current *PendingUpgrade) bool {
	if expected == nil || current == nil || current.Phase != PendingUpgradePhasePrepared || expected.CodexSourceSwitch == nil || current.CodexSourceSwitch == nil || *expected.CodexSourceSwitch != *current.CodexSourceSwitch {
		return false
	}
	if expected.RequestedAt == nil || current.RequestedAt == nil {
		return expected.RequestedAt == nil && current.RequestedAt == nil
	}
	return expected.RequestedAt.Equal(*current.RequestedAt)
}

func applyCodexSourceSwitchConfig(state InstallState) error {
	if err := validateCodexSourceSwitchConfig(state); err != nil {
		return err
	}
	raw, err := os.ReadFile(state.ConfigPath)
	if err != nil {
		return err
	}
	if codexSourceDigest(raw) != state.PendingUpgrade.CodexSourceSwitch.ConfigDigest {
		return fmt.Errorf("Codex source configuration changed before apply")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil || root == nil {
		return fmt.Errorf("invalid Codex source config object: %v", err)
	}
	wrapper, err := codexSourceJSONMap(root["wrapper"])
	if err != nil {
		return err
	}
	runtime, err := codexSourceJSONMap(wrapper["codexRuntime"])
	if err != nil {
		return err
	}
	runtime["source"], _ = json.Marshal(state.PendingUpgrade.CodexSourceSwitch.TargetSource)
	wrapper["codexRuntime"], _ = json.Marshal(runtime)
	root["wrapper"], _ = json.Marshal(wrapper)
	updated, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(state.ConfigPath, append(updated, '\n'), 0o600)
}

func codexSourceJSONMap(raw json.RawMessage) (map[string]json.RawMessage, error) {
	result := map[string]json.RawMessage{}
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return result, nil
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("invalid Codex source settings object: %w", err)
	}
	return result, nil
}
