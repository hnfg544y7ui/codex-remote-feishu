package wrapper

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/codexmanaged"
	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

type ensureManagedCodex func(context.Context, string, string, []string) (string, error)

type preparedCodexBinaryKey struct{}

func (a *App) selectCodexChildBinary(ctx context.Context, env []string, ensure ensureManagedCodex) (string, error) {
	if binary, ok := ctx.Value(preparedCodexBinaryKey{}).(string); ok {
		return binary, nil
	}
	if a.config.CodexRuntime.Source == config.CodexRuntimeSourceExternal {
		return a.config.CodexRealBinary, nil
	}
	if a.config.CodexRuntime.Source == config.CodexRuntimeSourceManaged {
		if strings.TrimSpace(a.config.RuntimePaths.StateDir) == "" {
			return "", fmt.Errorf("managed Codex requires a private runtime state directory")
		}
		// Only the daemon installs and activates versions. Child startup resolves
		// the persisted active release without contacting the registry.
		resolveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		release, err := codexmanaged.Resolve(resolveCtx, filepath.Join(a.config.RuntimePaths.StateDir, "codex-runtime"), env)
		if err != nil {
			return "", fmt.Errorf("resolve managed Codex child (start daemon to initialize the runtime): %w", err)
		}
		return release.Binary, nil
	}
	if !a.config.Managed || a.config.Lifetime != string(lifetimeDaemonOwned) ||
		!state.IsInstanceSource(a.config.Source, state.InstanceSourceHeadless) {
		return a.config.CodexRealBinary, nil
	}
	if strings.TrimSpace(a.config.RuntimePaths.StateDir) == "" {
		return "", fmt.Errorf("daemon-owned Codex requires a private runtime state directory")
	}
	// The daemon warms updates before its pool starts and every fifteen minutes.
	// Child startup must also fit the existing restart and headless watchdogs.
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if ensure == nil {
		ensure = codexmanaged.EnsureWithEnv
	}
	binary, err := ensure(checkCtx, a.config.CodexRealBinary, filepath.Join(a.config.RuntimePaths.StateDir, "codex-runtime"), env)
	if err != nil {
		return "", fmt.Errorf("prepare private Codex child: %w", err)
	}
	return binary, nil
}
