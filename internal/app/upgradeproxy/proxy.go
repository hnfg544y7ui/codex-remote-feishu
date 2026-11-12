// Package upgradeproxy isolates upgrade networking from Feishu/model proxies.
package upgradeproxy

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
)

var errNoAutoProxy = errors.New("no Windows automatic proxy deployment")

const (
	System = "system"
	Direct = "direct"
)

// Normalize accepts the zero value as the default system policy.
func Normalize(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = System
	}
	if mode != System && mode != Direct {
		return "", fmt.Errorf("unsupported upgrade proxy mode %q", mode)
	}
	return mode, nil
}

// NewClient creates an independent transport. It never changes DefaultTransport
// or reads the process-wide proxy cache. Windows reads current user settings for
// each request, including redirects; other platforms use capturedEnv.
func NewClient(mode string, capturedEnv []string) (*http.Client, error) {
	mode, err := Normalize(mode)
	if err != nil {
		return nil, err
	}
	var proxy func(*http.Request) (*url.URL, error)
	if mode == System {
		proxy = systemProxy(append([]string(nil), capturedEnv...))
	}
	return clientWithProxy(proxy), nil
}

func clientWithProxy(proxy func(*http.Request) (*url.URL, error)) *http.Client {
	base := &http.Transport{Proxy: proxy}
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		base = defaultTransport.Clone()
		base.Proxy = proxy
	}
	return &http.Client{Transport: base}
}

// Env returns a private subprocess environment for the selected policy. On
// Windows this exports current manual proxy settings. PAC cannot be represented
// faithfully as environment variables and returns an explicit error.
func Env(mode string, capturedEnv []string) ([]string, error) {
	mode, err := Normalize(mode)
	if err != nil {
		return nil, err
	}
	if mode == Direct {
		return append(clearProxyEnv(capturedEnv), "npm_config_proxy=false", "npm_config_https_proxy=false", "npm_config_noproxy=localhost,127.0.0.1,::1"), nil
	}
	return systemEnv(append([]string(nil), capturedEnv...))
}

func clearProxyEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToLower(key) {
		case "http_proxy", "https_proxy", "all_proxy", "no_proxy", "npm_config_proxy", "npm_config_https_proxy", "npm_config_noproxy":
			continue
		}
		out = append(out, entry)
	}
	return out
}

func envValue(env []string, names ...string) string {
	for _, name := range names {
		for _, entry := range env {
			key, value, _ := strings.Cut(entry, "=")
			if key == name && value != "" {
				return value
			}
		}
	}
	return ""
}

func envProxy(env []string) func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		if bypass(req.URL, envValue(env, "NO_PROXY", "no_proxy"), false) {
			return nil, nil
		}
		var address string
		if req.URL.Scheme == "https" {
			address = envValue(env, "HTTPS_PROXY", "https_proxy")
		} else if req.URL.Scheme == "http" {
			address = envValue(env, "HTTP_PROXY", "http_proxy")
		}
		if address == "" {
			address = envValue(env, "ALL_PROXY", "all_proxy")
		}
		return parseProxy(address)
	}
}

func parseProxy(address string) (*url.URL, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, nil
	}
	if !strings.Contains(address, "://") {
		address = "http://" + address
	}
	u, err := url.Parse(address)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("invalid upgrade proxy address")
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("unsupported upgrade proxy scheme %q", u.Scheme)
	}
	return u, nil
}

func bypass(u *url.URL, list string, windows bool) bool {
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
		return true
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	for _, item := range strings.FieldsFunc(strings.ToLower(list), func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		if item == "*" || (windows && item == "<local>" && !strings.Contains(host, ".") && net.ParseIP(host) == nil) {
			return true
		}
		if _, subnet, err := net.ParseCIDR(item); err == nil {
			if subnet.Contains(net.ParseIP(host)) {
				return true
			}
			continue
		}
		pattern, itemPort, err := net.SplitHostPort(item)
		if err == nil {
			if itemPort != port {
				continue
			}
			item = pattern
		}
		if windows {
			if match, _ := path.Match(item, host); match {
				return true
			}
			continue
		}
		if host == strings.TrimPrefix(item, "*.") || host == strings.TrimPrefix(item, ".") || strings.HasSuffix(host, "."+strings.TrimLeft(item, "*.")) {
			return true
		}
	}
	return false
}

type settings struct {
	Proxy         string
	Bypass        string
	AutoConfigURL string
	AutoDetect    bool
}

func proxyForSettings(s settings, req *http.Request, auto func(settings, *http.Request) (*url.URL, error)) (*url.URL, error) {
	if bypass(req.URL, s.Bypass, true) {
		return nil, nil
	}
	if s.AutoConfigURL != "" || (s.Proxy == "" && s.AutoDetect) {
		proxy, err := auto(s, req)
		if s.AutoConfigURL == "" && errors.Is(err, errNoAutoProxy) {
			return nil, nil
		}
		return proxy, err
	}
	return manualProxy(s, req)
}

// Automatic proxy choices cannot be exported as a process-wide npm proxy.
// However, Windows commonly enables discovery even when no WPAD deployment
// exists. That positively identified case is the ordinary direct system policy.
func automaticEnv(s settings, env []string, detect func() (bool, error)) ([]string, error) {
	if s.Proxy == "" && s.AutoConfigURL == "" && s.AutoDetect {
		deployed, err := detect()
		if err != nil {
			return nil, err
		}
		if !deployed {
			s.AutoDetect = false
		}
	}
	return manualEnv(s, env)
}

func manualProxy(s settings, req *http.Request) (*url.URL, error) {
	if bypass(req.URL, s.Bypass, true) {
		return nil, nil
	}
	address := s.Proxy
	if strings.Contains(address, "=") {
		address = ""
		hasSOCKS := false
		for _, entry := range strings.Split(s.Proxy, ";") {
			scheme, value, ok := strings.Cut(strings.TrimSpace(entry), "=")
			if ok && strings.EqualFold(strings.TrimSpace(scheme), "socks") {
				hasSOCKS = true
			}
			if ok && strings.EqualFold(strings.TrimSpace(scheme), req.URL.Scheme) {
				address = strings.TrimSpace(value)
			}
		}
		if address == "" && hasSOCKS {
			return nil, fmt.Errorf("Windows SOCKS fallback is not supported for upgrade downloads; configure an HTTP/HTTPS system proxy or select direct mode")
		}
	}
	if parts := strings.FieldsFunc(address, func(r rune) bool { return r == ';' || r == ' ' }); len(parts) > 0 {
		address = parts[0]
	}
	return parseProxy(address)
}

func manualEnv(s settings, env []string) ([]string, error) {
	if s.Proxy == "" && (s.AutoConfigURL != "" || s.AutoDetect) {
		return nil, fmt.Errorf("Windows automatic proxy cannot be exported to npm environment; configure a manual system proxy or select direct mode")
	}
	if s.AutoConfigURL != "" {
		return nil, fmt.Errorf("Windows PAC proxy cannot be exported to npm environment; configure a manual system proxy or select direct mode")
	}
	out := append(clearProxyEnv(env), "npm_config_proxy=false", "npm_config_https_proxy=false", "npm_config_noproxy=localhost,127.0.0.1,::1")
	for _, scheme := range []string{"http", "https"} {
		u, _ := url.Parse(scheme + "://registry.npmjs.org")
		proxy, err := manualProxy(settings{Proxy: s.Proxy}, &http.Request{URL: u})
		if err != nil {
			return nil, err
		}
		if proxy != nil {
			if proxy.Scheme == "socks5" || proxy.Scheme == "socks5h" {
				return nil, fmt.Errorf("npm upgrades require an HTTP/HTTPS system proxy; SOCKS cannot be exported")
			}
			key := strings.ToUpper(scheme) + "_PROXY"
			out = append(out, key+"="+proxy.String())
			if scheme == "http" {
				out = replaceEnv(out, "npm_config_proxy", proxy.String())
			} else {
				out = replaceEnv(out, "npm_config_https_proxy", proxy.String())
			}
		}
	}
	list := strings.ReplaceAll(s.Bypass, ";", ",")
	list = strings.ReplaceAll(list, "<local>", "localhost,127.0.0.1,::1")
	if list != "" {
		out = append(out, "NO_PROXY="+list)
		out = replaceEnv(out, "npm_config_noproxy", list)
	}
	return out, nil
}

func replaceEnv(env []string, key, value string) []string {
	for i, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, key) {
			env[i] = key + "=" + value
			return env
		}
	}
	return append(env, key+"="+value)
}
