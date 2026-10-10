package install

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
