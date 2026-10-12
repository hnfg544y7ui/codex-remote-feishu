package wrapper

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func sharedResumeApp(url string) *App {
	cfg := sharedTestApp(url).config
	cfg.ResumeThreadID = "desktop-current-thread"
	return New(cfg)
}

func TestSharedCodexStartupSubscribesExactThreadWithoutStartingTurn(t *testing.T) {
	requests := make(chan []map[string]any, 1)
	url := sharedTestServer(t, func(conn *websocket.Conn) {
		var initialize, initialized, resume map[string]any
		if err := conn.ReadJSON(&initialize); err != nil {
			return
		}
		if err := conn.WriteJSON(map[string]any{"id": initialize["id"], "result": map[string]any{}}); err != nil {
			return
		}
		if err := conn.ReadJSON(&initialized); err != nil {
			return
		}
		if err := conn.ReadJSON(&resume); err != nil {
			return
		}
		requests <- []map[string]any{initialize, initialized, resume}
		// Real app-server can emit the subscribed thread's events before its
		// resume response. Preserve this order for ownership buffering.
		if err := conn.WriteJSON(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": "desktop-current-thread", "turn": map[string]any{"id": "desktop-current-turn"}}}); err != nil {
			return
		}
		if err := conn.WriteJSON(map[string]any{"id": resume["id"], "result": map[string]any{"thread": map[string]any{"id": "desktop-current-thread"}}}); err != nil {
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	app := sharedResumeApp(url)
	session, err := app.launchCodexChildSession(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stopChildSession(session, nil)
	if err := app.runtime.(*codexBackendRuntime).CheckSharedBackendTimeout(time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("startup scope not established before Launch returned: %v", err)
	}
	frames := <-requests
	for i, method := range []string{"initialize", "initialized", "thread/resume"} {
		if frames[i]["method"] != method {
			t.Fatalf("startup method order = %#v", frames)
		}
	}
	params := frames[2]["params"].(map[string]any)
	if len(params) != 2 || params["threadId"] != "desktop-current-thread" || params["excludeTurns"] != true {
		t.Fatalf("startup resume changed backend config: %#v", params)
	}
	reader := bufio.NewReader(session.stdout)
	var projected []agentproto.Event
	for i := 0; i < 1; i++ {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		result, err := app.runtime.ObserveServer(line)
		if err != nil {
			t.Fatal(err)
		}
		projected = append(projected, result.Events...)
		if len(result.OutboundToChild) != 0 {
			t.Fatal("startup scope triggered a model turn")
		}
	}
	if !batchHasEvent(projected, func(e agentproto.Event) bool {
		return e.Kind == agentproto.EventTurnStarted && e.ThreadID == "desktop-current-thread" && e.TurnID == "desktop-current-turn" && e.Initiator.Kind == agentproto.InitiatorLocalUI
	}) {
		t.Fatalf("existing desktop turn not projected after subscription: %#v", projected)
	}
	if frame, _, err := app.runtime.(*codexBackendRuntime).BuildSharedStartupResumeFrame(); err != nil || len(frame) != 0 {
		t.Fatal("startup subscription was replayed twice")
	}
}

func TestSharedCodexStartupRejectsScopeErrorOrWrongThread(t *testing.T) {
	for _, mode := range []string{"rejected", "wrong-thread"} {
		t.Run(mode, func(t *testing.T) {
			closed := make(chan struct{})
			url := sharedTestServer(t, func(conn *websocket.Conn) {
				defer close(closed)
				if _, err := sharedBootstrapReply(conn); err != nil {
					return
				}
				var resume map[string]any
				if err := conn.ReadJSON(&resume); err != nil {
					return
				}
				response := map[string]any{"id": resume["id"], "result": map[string]any{"thread": map[string]any{"id": "different-thread"}}}
				if mode == "rejected" {
					delete(response, "result")
					response["error"] = map[string]any{"code": -32000, "message": "thread unavailable"}
				}
				_ = conn.WriteJSON(response)
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
				}
			})
			_, err := sharedResumeApp(url).launchCodexChildSession(context.Background(), nil, nil)
			problem, ok := err.(agentproto.ErrorInfo)
			if !ok || problem.Code != "shared_backend_scope_failed" || !problem.Retryable {
				t.Fatalf("scope failure not recoverable: %#v", err)
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("failed scope retained a live connection")
			}
		})
	}
}

func TestSharedCodexStartupScopeWaitHonorsCancellation(t *testing.T) {
	closed := make(chan struct{})
	url := sharedTestServer(t, func(conn *websocket.Conn) {
		defer close(closed)
		if _, err := sharedBootstrapReply(conn); err != nil {
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := sharedResumeApp(url).launchCodexChildSession(ctx, nil, nil)
	problem, ok := err.(agentproto.ErrorInfo)
	if !ok || problem.Code != "shared_backend_scope_failed" || !problem.Retryable {
		t.Fatalf("cancelled scope not recoverable: %#v", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("scope cancellation leaked a connection")
	}
}

func TestSharedCodexStartupScopeBoundsReplayBuffer(t *testing.T) {
	app := sharedResumeApp("ws://127.0.0.1:3")
	line, _ := json.Marshal(map[string]any{"method": "other/broadcast", "params": map[string]any{"text": string(make([]byte, sharedBootstrapReplayLimit))}})
	_, err := app.bootstrapSharedResume(io.Discard, bytesReaderLine(line), nil, nil)
	if problem, ok := err.(agentproto.ErrorInfo); !ok || problem.Code != "shared_backend_scope_failed" {
		t.Fatalf("unbounded scope replay accepted: %v", err)
	}
}

func bytesReaderLine(line []byte) io.Reader { return bytes.NewReader(append(line, '\n')) }
