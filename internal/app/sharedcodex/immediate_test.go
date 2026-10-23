package sharedcodex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func immediateStatus() Status {
	return Status{Endpoint: "ws://127.0.0.1:17550", Version: "0.162.1", Generation: 7,
		State: "running", ImmediateUpgradeSupported: true, ImmediatePlan: &ImmediatePlan{
			Token: strings.Repeat("ab", 32), ExpiresAt: time.Now().UTC().Add(time.Minute),
			Version: "0.162.1", TargetVersion: "0.163.0", Generation: 7,
			ActiveThreads: 2, ActiveGoals: 1, UncertainRequests: 3, PendingRequests: 4,
			ThreadNames: []string{"当前任务", "another task"},
		}}
}

func TestImmediatePreviewAndExecutionWire(t *testing.T) {
	previewed, executed, normal := false, false, false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("ab", 32) || r.Header.Get("Origin") != "" {
			t.Error("preview/execution must use enrolled bearer and no browser origin")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["version"] != "0.163.0" || body["expectedVersion"] != "0.162.1" {
			t.Errorf("unbound upgrade target: %+v", body)
		}
		switch r.URL.Path {
		case "/v1/upgrade/immediate/preview":
			previewed = true
			if len(body) != 2 {
				t.Errorf("preview included execution fields: %+v", body)
			}
		case "/v1/upgrade":
			if body["immediate"] == true {
				executed = true
				if len(body) != 5 || body["confirmationToken"] != strings.Repeat("ab", 32) || body["rollback"] != false {
					t.Errorf("wrong immediate execution contract: %+v", body)
				}
			} else {
				normal = true
				if len(body) != 3 || body["rollback"] != false {
					t.Errorf("ordinary upgrade wire changed: %+v", body)
				}
			}
		default:
			t.Error("unexpected control route")
		}
		_ = json.NewEncoder(w).Encode(immediateStatus())
	}))
	defer s.Close()
	c, _ := enrolled(t, "ws://127.0.0.1:17550", s.URL)
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	u := Upgrade{Version: "0.163.0", ExpectedVersion: "0.162.1"}
	plan, err := c.PreviewImmediate(context.Background(), u)
	if err != nil || plan.ActiveThreads != 2 || plan.ActiveGoals != 1 || plan.UncertainRequests != 3 || plan.PendingRequests != 4 || len(plan.ThreadNames) != 2 {
		t.Fatalf("preview: %+v %v", plan, err)
	}
	if _, err := c.Upgrade(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	u.Immediate, u.ConfirmationToken = true, plan.Token
	if _, err := c.Upgrade(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if !previewed || !executed || !normal {
		t.Fatal("maintenance requests did not reach owner")
	}
}

func TestImmediatePreviewRejectsUnboundOrExpiredPlan(t *testing.T) {
	cases := map[string]func(*Status){
		"legacy capability":    func(s *Status) { s.ImmediateUpgradeSupported = false },
		"missing plan":         func(s *Status) { s.ImmediatePlan = nil },
		"stale owner":          func(s *Status) { s.Version = "0.161.0" },
		"wrong plan version":   func(s *Status) { s.ImmediatePlan.Version = "0.161.0" },
		"wrong target":         func(s *Status) { s.ImmediatePlan.TargetVersion = "0.164.0" },
		"changed generation":   func(s *Status) { s.ImmediatePlan.Generation++ },
		"unknown generation":   func(s *Status) { s.Generation, s.ImmediatePlan.Generation = 0, 0 },
		"missing token":        func(s *Status) { s.ImmediatePlan.Token = "" },
		"bad token":            func(s *Status) { s.ImmediatePlan.Token = strings.Repeat("zz", 32) },
		"expired":              func(s *Status) { s.ImmediatePlan.ExpiresAt = time.Now().Add(-time.Second) },
		"unbounded lifetime":   func(s *Status) { s.ImmediatePlan.ExpiresAt = time.Now().Add(time.Hour) },
		"negative threads":     func(s *Status) { s.ImmediatePlan.ActiveThreads = -1 },
		"negative goals":       func(s *Status) { s.ImmediatePlan.ActiveGoals = -1 },
		"negative uncertainty": func(s *Status) { s.ImmediatePlan.UncertainRequests = -1 },
		"negative requests":    func(s *Status) { s.ImmediatePlan.PendingRequests = -1 },
		"wrong identity":       func(s *Status) { s.Endpoint = "ws://127.0.0.1:9999" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				status := immediateStatus()
				mutate(&status)
				_ = json.NewEncoder(w).Encode(status)
			}))
			defer s.Close()
			c, _ := enrolled(t, "ws://127.0.0.1:17550", s.URL)
			if _, err := c.PreviewImmediate(context.Background(), Upgrade{Version: "0.163.0", ExpectedVersion: "0.162.1"}); err == nil {
				t.Fatal("invalid owner preview accepted")
			}
		})
	}
}

func TestImmediateRejectsInvalidRequestsBeforeSending(t *testing.T) {
	c := &Client{} // A valid local rejection must not access an HTTP client.
	for _, u := range []Upgrade{
		{Immediate: true},
		{Immediate: true, ConfirmationToken: strings.Repeat("z", 64)},
		{Immediate: true, ConfirmationToken: strings.Repeat("a", 64), Rollback: true},
		{ConfirmationToken: strings.Repeat("a", 64)},
	} {
		if _, err := c.Upgrade(context.Background(), u); err == nil {
			t.Fatal("invalid immediate execution accepted")
		}
	}
	for _, u := range []Upgrade{
		{}, {Version: "0.163.0"}, {ExpectedVersion: "0.162.1"},
		{Version: "0.163.0", ExpectedVersion: "0.162.1", Rollback: true},
		{Version: "0.163.0", ExpectedVersion: "0.162.1", Immediate: true},
		{Version: "0.163.0", ExpectedVersion: "0.162.1", ConfirmationToken: "unexpected"},
	} {
		if _, err := c.PreviewImmediate(context.Background(), u); err == nil {
			t.Fatal("execution fields accepted as preview request")
		}
	}
}

func TestImmediatePreviewRejectsUnsafeResponses(t *testing.T) {
	for _, mode := range []string{"redirect", "oversized", "trailing JSON", "malformed", "invalid expiry", "unauthorized", "origin rejected", "legacy status"} {
		t.Run(mode, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "redirect":
					http.Redirect(w, r, "http://127.0.0.1:1", http.StatusTemporaryRedirect)
					return
				case "unauthorized", "origin rejected":
					w.WriteHeader(http.StatusUnauthorized)
				case "malformed":
					_, _ = w.Write([]byte(`{"generation":`))
					return
				case "invalid expiry":
					_, _ = w.Write([]byte(`{"endpoint":"ws://127.0.0.1:17550","version":"0.162.1","immediatePlan":{"expiresAt":"tomorrow"}}`))
					return
				case "legacy status":
					_ = json.NewEncoder(w).Encode(Status{Endpoint: "ws://127.0.0.1:17550", Version: "0.162.1"})
					return
				}
				_ = json.NewEncoder(w).Encode(immediateStatus())
				if mode == "oversized" {
					_, _ = w.Write([]byte(strings.Repeat(" ", 16384)))
				} else if mode == "trailing JSON" {
					_, _ = w.Write([]byte(`{}`))
				}
			}))
			defer s.Close()
			c, _ := enrolled(t, "ws://127.0.0.1:17550", s.URL)
			if _, err := c.PreviewImmediate(context.Background(), Upgrade{Version: "0.163.0", ExpectedVersion: "0.162.1"}); err == nil {
				t.Fatal("unsafe control response accepted")
			}
		})
	}
}
