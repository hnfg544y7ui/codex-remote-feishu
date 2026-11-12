//go:build !windows

package upgradeproxy

import "testing"

func TestSystemEnvUsesCapturedProxyAndOverridesNpmrc(t *testing.T) {
	got, err := Env(System, []string{"PATH=kept", "https_proxy=http://captured:7890", "npm_config_https_proxy=http://stale:1", "NO_PROXY=localhost,.internal"})
	if err != nil {
		t.Fatal(err)
	}
	if envValue(got, "HTTPS_PROXY") != "http://captured:7890" || envValue(got, "npm_config_https_proxy") != "http://captured:7890" {
		t.Fatal("captured proxy not applied")
	}
	if envValue(got, "PATH") != "kept" || envValue(got, "npm_config_proxy") != "false" || envValue(got, "npm_config_noproxy") != "localhost,.internal" {
		t.Fatal("unexpected export")
	}
}
