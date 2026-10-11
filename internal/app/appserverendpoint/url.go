package appserverendpoint

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// NormalizeURL accepts only unauthenticated, explicitly addressed loopback WS.
// A shared app-server can execute local tools, so no remote address is allowed.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "ws" || u.User != nil || u.Opaque != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") ||
		(u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return "", fmt.Errorf("shared Codex app-server URL must be ws://127.0.0.1:PORT or ws://[::1]:PORT")
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || (host != "127.0.0.1" && host != "::1") {
		return "", fmt.Errorf("shared Codex app-server requires an explicit loopback address and port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return "", fmt.Errorf("shared Codex app-server port must be an integer between 1 and 65535")
	}
	return "ws://" + net.JoinHostPort(host, port), nil
}
