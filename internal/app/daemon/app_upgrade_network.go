package daemon

import (
	"net/http"

	"github.com/kxn/codex-remote-feishu/internal/app/upgradeproxy"
)

// Upgrade traffic has its own transport. The daemon may have cleared process
// proxy variables to keep Feishu direct; neither that nor a model's proxy policy
// should decide how release metadata and archives are downloaded.
func (a *App) upgradeHTTPClient() (*http.Client, error) {
	mode, err := a.upgradeProxyMode()
	if err != nil {
		return nil, err
	}
	cfg := a.managedCodexConfig()
	return upgradeproxy.NewClient(mode, cfg.BaseEnv)
}
