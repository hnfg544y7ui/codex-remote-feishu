package install

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/config"
)

func TestObserveUpgradeProbesAPIOriginInsteadOfAdminPage(t *testing.T) {
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path] = true
		switch r.URL.Path {
		case "/healthz", "/api/admin/runtime-status", "/v1/status":
			w.WriteHeader(http.StatusOK)
		case "/api/admin/bootstrap-state":
			_, _ = w.Write([]byte(`{"setupRequired":false,"gateways":[{"state":"connected"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultAppConfig()
	cfg.Admin.ListenHost, cfg.Admin.ListenPort = host, number
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err = observeUpgrade(ctx, config.LoadedAppConfig{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/healthz", "/api/admin/bootstrap-state", "/api/admin/runtime-status", "/v1/status"} {
		if !seen[path] {
			t.Errorf("health endpoint %s was not probed", path)
		}
	}
}

func TestObserveUpgradeAllowsGatewayRecoveryAfterTenSeconds(t *testing.T) {
	var ready atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/admin/bootstrap-state" {
			state := "connecting"
			if ready.Load() {
				state = "connected"
			}
			_, _ = w.Write([]byte(`{"setupRequired":false,"gateways":[{"state":"` + state + `"}]}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultAppConfig()
	cfg.Admin.ListenHost, cfg.Admin.ListenPort = host, number
	timer := time.AfterFunc(11*time.Second, func() { ready.Store(true) })
	defer timer.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := observeUpgrade(ctx, config.LoadedAppConfig{Config: cfg}); err != nil {
		t.Fatalf("gateway recovered within startup budget: %v", err)
	}
}
