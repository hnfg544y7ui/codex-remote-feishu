package daemon

import (
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/codexmanaged"
)

const (
	upgradeMetadataTimeout     = 60 * time.Second
	upgradePrepareTimeout      = 10 * time.Minute
	codexUpgradeCheckTimeout   = 60 * time.Second
	codexUpgradeInstallTimeout = codexmanaged.UpgradeTimeout
)
