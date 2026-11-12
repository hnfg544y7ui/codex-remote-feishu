package codexmanaged

import (
	"context"
	"net/http"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/upgradeproxy"
)

// An empty mode preserves existing non-upgrade callers. Explicit upgrade policy
// scopes HTTP/npm to the download, while binary validation keeps the native env.
func managedUpgradeOperations(mode string, env []string) (managedOperations, error) {
	env = append([]string{}, env...)
	ops := managedOperationsWithEnv(env)
	if mode == "" {
		return ops, nil
	}
	mode, err := upgradeproxy.Normalize(mode)
	if err != nil {
		return managedOperations{}, err
	}
	ops.lookup = func(ctx context.Context, channel string) (string, error) {
		client, err := upgradeproxy.NewClient(mode, env)
		if err != nil {
			return "", err
		}
		defer client.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		tag := "latest"
		if channel == "alpha" {
			tag = "alpha"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://registry.npmjs.org/@openai%2fcodex/"+tag, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Accept", "application/json")
		return lookupManagedChannelRequest(req, client)
	}
	ops.install = func(ctx context.Context, prefix, target string) error {
		networkEnv, err := upgradeproxy.Env(mode, env)
		if err != nil {
			return err
		}
		return installPrivateWithEnv(ctx, prefix, target, networkEnv)
	}
	return ops, nil
}
