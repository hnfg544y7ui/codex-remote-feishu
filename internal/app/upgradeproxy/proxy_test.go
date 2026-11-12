package upgradeproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	for input, want := range map[string]string{"": System, " SYSTEM ": System, "direct": Direct} {
		got, err := Normalize(input)
		if err != nil || got != want {
			t.Fatalf("%q got %q %v", input, got, err)
		}
	}
	if _, err := Normalize("off"); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func TestDirectClientAndEnvNeverUseInheritedProxy(t *testing.T) {
	env := []string{"PATH=preserved", "HTTPS_PROXY=http://unreachable:1", "http_proxy=http://unreachable:2", "ALL_PROXY=socks5://unreachable:3", "NPM_CONFIG_PROXY=http://unreachable:4", "npm_config_https_proxy=http://unreachable:5", "NO_PROXY=*"}
	copyEnv := append([]string(nil), env...)
	client, err := NewClient(Direct, env)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer target.Close()
	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("direct transport has proxy resolver")
	}
	got, err := Env(Direct, env)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range got {
		key, value, _ := strings.Cut(entry, "=")
		if strings.Contains(strings.ToLower(key), "proxy") && value != "false" && !(key == "npm_config_noproxy" && value == "localhost,127.0.0.1,::1") {
			t.Fatalf("proxy remained %s", key)
		}
	}
	if envValue(got, "PATH") != "preserved" || !reflect.DeepEqual(env, copyEnv) {
		t.Fatal("base env changed")
	}
	for _, key := range []string{"npm_config_proxy", "npm_config_https_proxy"} {
		present := false
		for _, entry := range got {
			if entry == key+"=false" {
				present = true
			}
		}
		if !present {
			t.Fatalf("missing explicit npmrc override %s", key)
		}
	}
}

func TestProxyResolverFollowsRedirectAndSettingsChange(t *testing.T) {
	var hosts []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hosts = append(hosts, r.URL.Hostname())
		if len(hosts) == 1 {
			http.Redirect(w, r, "http://asset.invalid/archive", 302)
		} else {
			io.WriteString(w, "archive")
		}
	}))
	defer proxy.Close()
	current := settings{Proxy: strings.TrimPrefix(proxy.URL, "http://")}
	var resolves []string
	client := clientWithProxy(func(req *http.Request) (*url.URL, error) {
		resolves = append(resolves, req.URL.Hostname())
		return manualProxy(current, req)
	})
	defer client.CloseIdleConnections()
	resp, err := client.Get("http://release.invalid/download")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(data) != "archive" || !reflect.DeepEqual(hosts, []string{"release.invalid", "asset.invalid"}) || !reflect.DeepEqual(resolves, hosts) {
		t.Fatalf("redirect bypassed resolver: hosts=%v resolves=%v", hosts, resolves)
	}
	current.Bypass = "asset.invalid"
	u, _ := url.Parse("http://asset.invalid/archive")
	got, err := client.Transport.(*http.Transport).Proxy(&http.Request{URL: u})
	if err != nil || got != nil {
		t.Fatal("updated bypass not used")
	}
}

func TestManualProxySchemesAndBypass(t *testing.T) {
	s := settings{Proxy: "http=proxy.local:8080;https=secure.local:8081", Bypass: "<local>;*.internal;192.168.0.0/16;private.example:8443"}
	for _, tc := range []struct{ address, want string }{{"http://public.example", "http://proxy.local:8080"}, {"https://public.example", "http://secure.local:8081"}, {"https://host.internal", ""}, {"https://shortname", ""}, {"https://192.168.12.1", ""}, {"https://localhost", ""}, {"https://private.example:8443", ""}, {"https://private.example:443", "http://secure.local:8081"}} {
		u, _ := url.Parse(tc.address)
		got, err := manualProxy(s, &http.Request{URL: u})
		if err != nil {
			t.Fatal(err)
		}
		value := ""
		if got != nil {
			value = got.String()
		}
		if value != tc.want {
			t.Fatalf("%s got %s want %s", tc.address, value, tc.want)
		}
	}
}

func TestPACIsResolvedPerURLAndNeverExportedSilently(t *testing.T) {
	u, _ := url.Parse("https://public.example")
	req := &http.Request{URL: u}
	called := false
	_, err := proxyForSettings(settings{Proxy: "manual:8080", AutoConfigURL: "https://pac.example/proxy.pac"}, req, func(s settings, r *http.Request) (*url.URL, error) {
		called = true
		return url.Parse("http://pac-proxy:8080")
	})
	if err != nil || !called {
		t.Fatal("PAC did not override manual proxy")
	}
	if _, err := manualEnv(settings{AutoConfigURL: "https://pac.example/proxy.pac"}, nil); err == nil {
		t.Fatal("PAC silently converted to environment")
	}
}

func TestEnvironmentResolverUsesCapturedEnvAndNoGlobalCache(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://wrong-global:1")
	u, _ := url.Parse("https://public.example")
	req := &http.Request{URL: u}
	got, err := envProxy([]string{"HTTPS_PROXY=http://captured:8080"})(req)
	if err != nil || got.Host != "captured:8080" {
		t.Fatalf("wrong proxy %v %v", got, err)
	}
	none, err := envProxy([]string{"HTTPS_PROXY=http://captured:8080", "NO_PROXY=.example"})(req)
	if err != nil || none != nil {
		t.Fatal("captured bypass ignored")
	}
}

func TestManualEnvOverridesInheritedAndNpmConfig(t *testing.T) {
	env := []string{"PATH=preserved", "HTTPS_PROXY=http://stale:1", "npm_config_proxy=http://stale:2", "npm_config_https_proxy=http://stale:3", "NPM_CONFIG_NOPROXY=*"}
	got, err := manualEnv(settings{Proxy: "127.0.0.1:7890", Bypass: "localhost;*.internal"}, env)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "npm_config_proxy", "npm_config_https_proxy"} {
		if envValue(got, key) != "http://127.0.0.1:7890" {
			t.Fatalf("missing system override %s", key)
		}
	}
	if envValue(got, "PATH") != "preserved" || envValue(got, "NO_PROXY") != "localhost,*.internal" {
		t.Fatal("env not preserved correctly")
	}
	if strings.Contains(strings.Join(got, "\n"), "stale") || strings.Contains(strings.Join(got, "\n"), "NOPROXY=*") {
		t.Fatal("stale config survived")
	}
}

func TestAutomaticEnvNoDeploymentVersusActualPAC(t *testing.T) {
	s := settings{AutoDetect: true}
	got, err := automaticEnv(s, []string{"PATH=kept", "HTTPS_PROXY=http://stale:7890"}, func() (bool, error) { return false, nil })
	if err != nil || envValue(got, "npm_config_https_proxy") != "false" || envValue(got, "HTTPS_PROXY") != "" {
		t.Fatal("absent WPAD must use direct system settings")
	}
	if _, err := automaticEnv(s, nil, func() (bool, error) { return true, nil }); err == nil {
		t.Fatal("actual WPAD cannot be exported globally")
	}
	u, _ := url.Parse("https://public.example")
	proxy, err := proxyForSettings(s, &http.Request{URL: u}, func(settings, *http.Request) (*url.URL, error) { return nil, errNoAutoProxy })
	if err != nil || proxy != nil {
		t.Fatal("HTTP should permit confirmed no-WPAD direct access")
	}
}

func TestWindowsSocksFallbackNeverSilentlyGoesDirect(t *testing.T) {
	u, _ := url.Parse("https://public.example")
	for _, value := range []string{"socks=proxy.internal:1080", "http=manual:8080;socks=proxy.internal:1080"} {
		if _, err := manualProxy(settings{Proxy: value}, &http.Request{URL: u}); err == nil {
			t.Fatal("unsupported SOCKS fallback was silently bypassed")
		}
	}
	if _, err := manualEnv(settings{Proxy: "socks5://proxy.internal:1080"}, nil); err == nil {
		t.Fatal("SOCKS silently exported to npm")
	}
}
