package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/app/install"
	"github.com/kxn/codex-remote-feishu/internal/config"
)

func directUpgradeNetworkFixture(t *testing.T) (*App, string) {
	t.Helper()
	a, path := newUpgradeTestApp(t, newLifecycleGateway())
	a.admin.loadConfig = func() (config.LoadedAppConfig, error) {
		return config.LoadedAppConfig{Config: config.AppConfig{Upgrade: config.UpgradeSettings{ProxyMode: "direct"}}}, nil
	}
	a.headlessRuntime.BaseEnv = []string{"HTTPS_PROXY=http://127.0.0.1:1", "HTTP_PROXY=http://127.0.0.1:1", "MODEL_SETTING=preserved"}
	return a, path
}

func TestUpgradeNetworkDirectMetadataAndDevIgnoreModelProxy(t *testing.T) {
	a, _ := directUpgradeNetworkFixture(t)
	before := append([]string(nil), a.headlessRuntime.BaseEnv...)
	releaseCalls, manifestCalls := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			releaseCalls++
			_ = json.NewEncoder(w).Encode([]install.ReleaseInfo{{TagName: "v1.5.0.1"}})
		case "/manifest":
			manifestCalls++
			_ = json.NewEncoder(w).Encode(install.DevManifest{Version: "dev-proxy-test", Assets: []install.DevManifestAsset{{
				GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Name: "candidate.zip", URL: "https://assets.invalid/candidate.zip",
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv(config.CodexRemoteReleasesAPIURLEnv, srv.URL+"/releases")
	t.Setenv(config.CodexRemoteDevManifestURLEnv, srv.URL+"/manifest")
	client, err := a.upgradeHTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	if tr, ok := client.Transport.(*http.Transport); !ok || tr.Proxy != nil {
		t.Fatalf("direct upgrades retained a proxy: %#v", client.Transport)
	}
	release, err := a.defaultReleaseLookup(context.Background(), install.ReleaseTrackProduction)
	if err != nil || release.TagName != "v1.5.0.1" {
		t.Fatalf("release lookup: %+v %v", release, err)
	}
	manifest, _, err := a.defaultDevManifestLookup(context.Background())
	if err != nil || manifest.Version != "dev-proxy-test" || releaseCalls != 1 || manifestCalls != 1 {
		t.Fatalf("dev lookup: %+v calls=%d/%d err=%v", manifest, releaseCalls, manifestCalls, err)
	}
	if !reflect.DeepEqual(before, a.headlessRuntime.BaseEnv) {
		t.Fatal("upgrade transport changed model environment")
	}
}

func TestUpgradeNetworkArchiveDownloadUsesSelectedTransport(t *testing.T) {
	for _, source := range []install.UpgradeSource{install.UpgradeSourceRelease, install.UpgradeSourceDev} {
		t.Run(string(source), func(t *testing.T) {
			a, statePath := directUpgradeNetworkFixture(t)
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				// Stop before archive extraction, shim creation or service mutation.
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer srv.Close()
			t.Setenv(config.CodexRemoteBaseURLEnv, srv.URL)
			stateValue, err := install.LoadState(statePath)
			if err != nil {
				t.Fatal(err)
			}
			stateValue.PendingUpgrade = &install.PendingUpgrade{Source: source, TargetVersion: "v1.5.0.1"}
			if source == install.UpgradeSourceDev {
				a.upgradeRuntime.DevManifest = func(context.Context) (install.DevManifest, install.DevManifestAsset, error) {
					return install.DevManifest{Version: "v1.5.0.1"}, install.DevManifestAsset{Name: "candidate.zip", URL: srv.URL + "/archive"}, nil
				}
			}
			a.runPendingUpgradeStart(upgradeStartRequest{State: stateValue})
			if calls != 1 {
				t.Fatalf("archive request calls=%d, expected direct request despite model proxy", calls)
			}
		})
	}
}
