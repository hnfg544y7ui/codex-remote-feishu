//go:build !windows

package upgradeproxy

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func systemProxy(env []string) func(*http.Request) (*url.URL, error) { return envProxy(env) }
func systemEnv(env []string) ([]string, error) {
	out := append(clearProxyEnv(env), "npm_config_proxy=false", "npm_config_https_proxy=false", "npm_config_noproxy=localhost,127.0.0.1,::1")
	for _, scheme := range []string{"http", "https"} {
		names := []string{"HTTP_PROXY", "http_proxy"}
		npmKey := "npm_config_proxy"
		if scheme == "https" {
			names = []string{"HTTPS_PROXY", "https_proxy"}
			npmKey = "npm_config_https_proxy"
		}
		value := envValue(env, names...)
		if value == "" {
			value = envValue(env, "ALL_PROXY", "all_proxy")
		}
		proxy, err := parseProxy(value)
		if err != nil {
			return nil, err
		}
		if proxy != nil {
			if proxy.Scheme == "socks5" || proxy.Scheme == "socks5h" {
				return nil, fmt.Errorf("npm upgrades require an HTTP/HTTPS proxy; SOCKS cannot be exported")
			}
			out = append(out, strings.ToUpper(scheme)+"_PROXY="+proxy.String())
			out = replaceEnv(out, npmKey, proxy.String())
		}
	}
	if value := envValue(env, "NO_PROXY", "no_proxy"); value != "" {
		out = append(out, "NO_PROXY="+value)
		out = replaceEnv(out, "npm_config_noproxy", value)
	}
	return out, nil
}
