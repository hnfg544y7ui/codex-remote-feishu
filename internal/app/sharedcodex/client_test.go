package sharedcodex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func enrolled(t *testing.T, endpoint, control string) (*Client, string) {
	t.Helper()
	root := t.TempDir()
	path := EnrollmentPath(root, endpoint)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	e := Enrollment{SchemaVersion: 1, Endpoint: endpoint, ControlURL: control, Token: strings.Repeat("ab", 32)}
	raw, _ := json.Marshal(e)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	secureTestEnrollment(t, path)
	c, err := Open(root, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return c, root
}

func TestOwnerStatusAndUpgradeAreAuthenticatedAndScoped(t *testing.T) {
	endpoint := "ws://127.0.0.1:17550"
	called := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("ab", 32) {
			t.Error("owner credential missing")
		}
		if r.Method == http.MethodPost {
			var u Upgrade
			if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
				t.Error(err)
			}
			if u.Version != "0.163.0" || u.ExpectedVersion != "0.162.1" {
				t.Errorf("wrong frozen target: %+v", u)
			}
			called = true
		}
		_ = json.NewEncoder(w).Encode(Status{Endpoint: endpoint, Version: "0.163.0", State: "running"})
	}))
	defer s.Close()
	c, _ := enrolled(t, endpoint, s.URL)
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	if _, err := c.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upgrade(context.Background(), Upgrade{Version: "0.163.0", ExpectedVersion: "0.162.1"}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("upgrade never reached enrolled owner")
	}
}

func TestOwnerRejectsWrongEndpointAndRedirect(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if redirect {
				http.Redirect(w, r, "http://127.0.0.1:1", http.StatusFound)
				return
			}
			_ = json.NewEncoder(w).Encode(Status{Endpoint: "ws://127.0.0.1:9999", Version: "0.162.1"})
		}))
		c, _ := enrolled(t, "ws://127.0.0.1:17550", s.URL)
		if _, err := c.Status(context.Background()); err == nil {
			t.Fatal("untrusted owner response accepted")
		}
		s.Close()
	}
}

func TestEnrollmentRejectsRemoteAndUnboundOwners(t *testing.T) {
	for _, raw := range []string{"http://localhost:1234", "https://127.0.0.1:1234", "http://192.0.2.1:1234", "http://127.0.0.1:1234/path", "http://user@127.0.0.1:1234", "http://127.0.0.1:1234?token=secret"} {
		if loopbackURL(raw, "http") {
			t.Errorf("accepted %s", raw)
		}
	}
	if _, err := Open(t.TempDir(), "ws://127.0.0.1:17550"); err == nil {
		t.Fatal("missing owner enrollment accepted")
	}
}
