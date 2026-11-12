package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/atomicfile"
	"github.com/kxn/codex-remote-feishu/internal/pathscope"
)

const (
	UpgradeProxySystem = "system"
	UpgradeProxyDirect = "direct"
)

type UpgradeSettings struct {
	ProxyMode string `json:"proxyMode,omitempty"`
}

// NormalizeUpgradeProxyMode keeps upgrade networking separate from Feishu and model traffic.
func NormalizeUpgradeProxyMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", UpgradeProxySystem:
		return UpgradeProxySystem, nil
	case UpgradeProxyDirect:
		return UpgradeProxyDirect, nil
	default:
		return "", fmt.Errorf("upgrade.proxyMode only supports system or direct")
	}
}

// WriteUpgradeProxyMode edits only upgrade.proxyMode, preserving unrelated and
// unknown JSON fields, including credentials from newer configuration versions.
func WriteUpgradeProxyMode(path, mode string) error {
	mode, err := NormalizeUpgradeProxyMode(mode)
	if err != nil {
		return err
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("config path is required")
	}
	if err := pathscope.EnsureWritePath(path); err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		raw, err = json.Marshal(DefaultAppConfig())
	}
	if err != nil {
		return err
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(raw, &data); err != nil {
		return err
	}
	if data == nil {
		return fmt.Errorf("config must be a JSON object")
	}
	upgrade := map[string]json.RawMessage{}
	if previous, ok := data["upgrade"]; ok && string(previous) != "null" {
		if err := json.Unmarshal(previous, &upgrade); err != nil {
			return err
		}
	}
	if upgrade == nil {
		upgrade = map[string]json.RawMessage{}
	}
	upgrade["proxyMode"], _ = json.Marshal(mode)
	data["upgrade"], err = json.Marshal(upgrade)
	if err != nil {
		return err
	}
	raw, err = json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return atomicfile.Write(path, append(raw, '\n'), 0600)
}
