package install

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

const upgradeHelperStartGateEnv = "CODEX_REMOTE_UPGRADE_START_GATE"

// A scheduled native helper cannot read or mutate the journal until its caller
// has acknowledged startup and committed ownership. Do not propagate this gate
// into the replacement daemon or any of that daemon's processes.
func waitForUpgradeHelperLaunch(ctx context.Context) error {
	gate := strings.TrimSpace(os.Getenv(upgradeHelperStartGateEnv))
	if gate == "" {
		return nil
	}
	defer os.Unsetenv(upgradeHelperStartGateEnv)
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := waitCtx.Err(); err != nil {
			return fmt.Errorf("upgrade startup ownership gate: %w", err)
		}
		data, err := os.ReadFile(gate)
		if err == nil {
			if string(data) == "committed" {
				return nil
			}
			return fmt.Errorf("upgrade startup ownership gate was not committed")
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("read upgrade startup ownership gate: %w", err)
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("upgrade startup ownership gate: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}
