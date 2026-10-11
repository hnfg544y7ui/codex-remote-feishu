package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	CodexRuntimeSourceManaged  = "managed"
	CodexRuntimeSourceExternal = "external"
	CodexRuntimeSourceLegacy   = "legacy"
	CodexRuntimeSourceEnv      = "CODEX_REMOTE_CODEX_RUNTIME_SOURCE"
	CodexRuntimeChannelEnv     = "CODEX_REMOTE_CODEX_RUNTIME_CHANNEL"
	CodexRuntimeVersionEnv     = "CODEX_REMOTE_CODEX_RUNTIME_VERSION"
	CodexRuntimeAutoUpdateEnv  = "CODEX_REMOTE_CODEX_RUNTIME_AUTO_UPDATE"
)

// CodexRuntimeSettings selects executable ownership, independently of CODEX_HOME
// and the CLI's existing credentials. Empty source retains the legacy behavior.
type CodexRuntimeSettings struct {
	Source     string `json:"source,omitempty"`
	Channel    string `json:"channel,omitempty"`
	Version    string `json:"version,omitempty"`
	AutoUpdate bool   `json:"autoUpdate,omitempty"`
}

var codexRuntimeVersionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)

func (s CodexRuntimeSettings) normalized() CodexRuntimeSettings {
	s.Source = strings.ToLower(strings.TrimSpace(s.Source))
	s.Channel = strings.ToLower(strings.TrimSpace(s.Channel))
	s.Version = strings.TrimSpace(s.Version)
	return s
}

func ValidateCodexRuntimeSettings(s CodexRuntimeSettings) error {
	s = s.normalized()
	switch s.Source {
	case "", CodexRuntimeSourceLegacy, CodexRuntimeSourceManaged, CodexRuntimeSourceExternal:
	default:
		return fmt.Errorf("codex runtime source must be managed, external or legacy")
	}
	switch s.Channel {
	case "", "stable", "alpha":
	default:
		return fmt.Errorf("codex runtime channel must be stable or alpha")
	}
	if s.Version != "" && !codexRuntimeVersionPattern.MatchString(s.Version) {
		return fmt.Errorf("codex runtime version must be an exact CLI version, such as 0.134.0 or 0.135.0-alpha.1")
	}
	return nil
}

// ResolveCodexRuntimeSettings applies explicitly present environment variables
// over JSON. An empty version override clears a pin; false disables auto-update.
// It never persists environment overrides back into the credential-bearing file.
func ResolveCodexRuntimeSettings(s CodexRuntimeSettings) (CodexRuntimeSettings, error) {
	for name, target := range map[string]*string{
		CodexRuntimeSourceEnv:  &s.Source,
		CodexRuntimeChannelEnv: &s.Channel,
		CodexRuntimeVersionEnv: &s.Version,
	} {
		if value, ok := os.LookupEnv(name); ok {
			*target = value
		}
	}
	if value, ok := os.LookupEnv(CodexRuntimeAutoUpdateEnv); ok {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return CodexRuntimeSettings{}, fmt.Errorf("%s must be a boolean: %w", CodexRuntimeAutoUpdateEnv, err)
		}
		s.AutoUpdate = parsed
	}
	s = s.normalized()
	if err := ValidateCodexRuntimeSettings(s); err != nil {
		return CodexRuntimeSettings{}, err
	}
	return s, nil
}
