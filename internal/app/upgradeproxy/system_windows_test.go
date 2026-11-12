package upgradeproxy

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"
)

// Reading current settings is local and read-only: this does not resolve PAC,
// contact any proxy, or change the current user's configuration.
func TestWindowsCurrentUserProxySettingsReadable(t *testing.T) {
	if _, err := readSystemSettings(); err != nil {
		t.Fatalf("current user proxy settings unavailable: %v", err)
	}
}

func TestWindowsSystemClientUsesCurrentSettingsNotCapturedEnv(t *testing.T) {
	s, err := readSystemSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.AutoConfigURL != "" || (s.AutoDetect && s.Proxy == "") {
		t.Skip("automatic proxy requires separate WinHTTP integration fixture")
	}
	client, err := NewClient(System, []string{"HTTPS_PROXY=http://wrong-stale-env:1"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	u, _ := url.Parse("https://registry.npmjs.org/")
	req := &http.Request{URL: u}
	got, err := client.Transport.(*http.Transport).Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	want, err := manualProxy(s, req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("client did not use current Windows settings")
	}
}
