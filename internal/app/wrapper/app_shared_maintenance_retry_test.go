package wrapper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestSharedCodexStartupRetriesMaintenanceBeforeResume(t *testing.T) {
	var attempts, resumes atomic.Int32
	url := sharedTestServer(t, func(conn *websocket.Conn) {
		if attempts.Add(1) == 1 {
			var initialize map[string]any
			if conn.ReadJSON(&initialize) == nil {
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "maintenance"), time.Now().Add(time.Second))
			}
			return
		}
		if _, err := sharedBootstrapReply(conn); err != nil {
			return
		}
		var resume map[string]any
		if err := conn.ReadJSON(&resume); err != nil {
			return
		}
		resumes.Add(1)
		if resume["method"] != "thread/resume" || resume["params"].(map[string]any)["threadId"] != "desktop-current-thread" {
			t.Error("maintenance retry changed or started the selected task")
			return
		}
		_ = conn.WriteJSON(map[string]any{"id": resume["id"], "result": map[string]any{"thread": map[string]any{"id": "desktop-current-thread"}}})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	session, err := sharedResumeApp(url).launchCodexChildSession(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("explicit maintenance refusal stranded startup: %v", err)
	}
	defer stopChildSession(session, nil)
	if attempts.Load() != 2 || resumes.Load() != 1 {
		t.Fatalf("attempts=%d resumes=%d; resume must execute once", attempts.Load(), resumes.Load())
	}
}

func TestSharedCodexStartupDoesNotRetryAfterResumeWasSent(t *testing.T) {
	var attempts atomic.Int32
	url := sharedTestServer(t, func(conn *websocket.Conn) {
		attempts.Add(1)
		if _, err := sharedBootstrapReply(conn); err != nil {
			return
		}
		var resume map[string]any
		if conn.ReadJSON(&resume) == nil {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "uncertain subscription"), time.Now().Add(time.Second))
		}
	})
	if session, err := sharedResumeApp(url).launchCodexChildSession(context.Background(), nil, nil); err == nil {
		stopChildSession(session, nil)
		t.Fatal("unconfirmed subscription was accepted")
	}
	if attempts.Load() != 1 {
		t.Fatal("subscription was replayed after an uncertain acknowledgement")
	}
}

func TestSharedCodexStartupMaintenanceRetryHonorsCancellation(t *testing.T) {
	var attempts atomic.Int32
	url := sharedTestServer(t, func(conn *websocket.Conn) {
		attempts.Add(1)
		var initialize map[string]any
		if conn.ReadJSON(&initialize) == nil {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "maintenance"), time.Now().Add(time.Second))
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	started := time.Now()
	if session, err := sharedTestApp(url).launchCodexChildSession(ctx, nil, nil); err == nil {
		stopChildSession(session, nil)
		t.Fatal("maintenance-only backend unexpectedly became ready")
	}
	if time.Since(started) > time.Second || attempts.Load() < 2 {
		t.Fatalf("unbounded or missing retry: elapsed=%v attempts=%d", time.Since(started), attempts.Load())
	}
}

func TestSharedCodexStartupRetriesHTTPMaintenanceRefusal(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := sharedBootstrapReply(conn); err != nil {
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	session, err := sharedTestApp("ws"+strings.TrimPrefix(server.URL, "http")).launchCodexChildSession(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stopChildSession(session, nil)
	if attempts.Load() != 2 {
		t.Fatalf("attempts=%d; HTTP 503 must retry before sending RPC", attempts.Load())
	}
}

func TestSharedCodexStartupOtherCloseDoesNotReplayInitialize(t *testing.T) {
	var attempts atomic.Int32
	url := sharedTestServer(t, func(conn *websocket.Conn) {
		attempts.Add(1)
		var initialize map[string]any
		if conn.ReadJSON(&initialize) == nil {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "failed"), time.Now().Add(time.Second))
		}
	})
	if session, err := sharedTestApp(url).launchCodexChildSession(context.Background(), nil, nil); err == nil {
		stopChildSession(session, nil)
		t.Fatal("non-maintenance failure was accepted")
	}
	if attempts.Load() != 1 {
		t.Fatal("non-maintenance failure retried")
	}
}

func TestSharedCodexStartupRetriesImmediateMaintenanceClose(t *testing.T) {
	var attempts atomic.Int32
	url := sharedTestServer(t, func(conn *websocket.Conn) {
		if attempts.Add(1) == 1 {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "frozen"), time.Now().Add(time.Second))
			return
		}
		if _, err := sharedBootstrapReply(conn); err != nil {
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	session, err := sharedTestApp(url).launchCodexChildSession(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("owner refused before reading initialize: %v", err)
	}
	defer stopChildSession(session, nil)
	if attempts.Load() != 2 {
		t.Fatalf("attempts=%d", attempts.Load())
	}
}
