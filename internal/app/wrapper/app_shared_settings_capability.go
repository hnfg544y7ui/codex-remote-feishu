package wrapper

import (
	"encoding/json"
	"regexp"
	"strconv"
)

// The native initialize response uses the connecting client's name in its
// first user-agent segment (for example codex-remote-owner), while its version
// is the server version. Never search later platform/client details for it.
var sharedCodexAgentVersion = regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9._-]*[/ ]v?(\d+)\.(\d+)\.(\d+)(?:[ +(-]|$)`)
var sharedCodexServerVersion = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:[-+].*)?$`)

func (r *codexBackendRuntime) observeSharedSettingsVersion(frame []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sharedThreadSettings = false
	if !r.sharedBackend {
		return
	}
	var response struct {
		Result struct {
			UserAgent  string `json:"userAgent"`
			ServerInfo struct {
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if json.Unmarshal(frame, &response) != nil {
		return
	}
	match := sharedCodexServerVersion.FindStringSubmatch(response.Result.ServerInfo.Version)
	if match == nil {
		match = sharedCodexAgentVersion.FindStringSubmatch(response.Result.UserAgent)
	}
	if len(match) != 4 {
		return
	}
	major, majorErr := strconv.Atoi(match[1])
	minor, minorErr := strconv.Atoi(match[2])
	_, patchErr := strconv.Atoi(match[3])
	r.sharedThreadSettings = majorErr == nil && minorErr == nil && patchErr == nil && (major > 0 || minor >= 170)
}
