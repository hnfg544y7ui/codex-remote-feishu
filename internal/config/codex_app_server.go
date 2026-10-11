package config

import (
	"fmt"
	"os"

	"github.com/kxn/codex-remote-feishu/internal/app/appserverendpoint"
)

// ResolveCodexAppServerURL applies an explicitly present environment override.
// An empty override restores the private stdio backend without rewriting JSON.
func ResolveCodexAppServerURL(configured string) (string, error) {
	if value, ok := os.LookupEnv(CodexAppServerURLEnv); ok {
		configured = value
	}
	endpoint, err := appserverendpoint.NormalizeURL(configured)
	if err != nil {
		return "", fmt.Errorf("codex app-server URL: %w", err)
	}
	return endpoint, nil
}
