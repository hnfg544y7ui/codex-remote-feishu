package wrapper

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kxn/codex-remote-feishu/internal/adapter/relayws"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func sharedTestServer(t *testing.T, handle func(*websocket.Conn)) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		handle(conn)
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

func sharedBootstrapReply(conn *websocket.Conn) (map[string]any, error) {
	var message map[string]any
	if err := conn.ReadJSON(&message); err != nil {
		return nil, err
	}
	if err := conn.WriteJSON(map[string]any{"id": message["id"], "result": map[string]any{"userAgent": "shared-test"}}); err != nil {
		return nil, err
	}
	var initialized map[string]any
	return message, conn.ReadJSON(&initialized)
}

func sharedTestApp(url string) *App {
	return New(Config{Backend: agentproto.BackendCodex, CodexAppServerURL: url, Args: []string{"app-server"}, Source: "headless", Version: "shared-test", CodexRealBinary: "/does/not/exist"})
}

func TestSharedCodexSessionBootstrapConcurrentWritesAndIndependentClose(t *testing.T) {
	initializations := make(chan map[string]any, 2)
	frames := make(chan map[string]any, 64)
	url := sharedTestServer(t, func(conn *websocket.Conn) {
		message, err := sharedBootstrapReply(conn)
		if err != nil {
			return
		}
		initializations <- message
		for {
			var frame map[string]any
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			frames <- frame
			if err := conn.WriteMessage(websocket.TextMessage, []byte("{\n \"id\":\"echo\", \"result\":{}\n}")); err != nil {
				return
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app := sharedTestApp(url)
	first, err := app.launchCodexChildSession(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stopChildSession(first, nil)
	second, err := app.launchCodexChildSession(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stopChildSession(second, nil)
	for _, session := range []*childSession{first, second} {
		if session.cmd != nil || !session.sharedBackend {
			t.Fatal("shared session unexpectedly owns a process")
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case initialize := <-initializations:
			client := initialize["params"].(map[string]any)["clientInfo"].(map[string]any)
			if client["name"] != "Codex Remote Headless" || client["version"] != "shared-test" {
				t.Fatalf("unexpected shared initialize: %#v", client)
			}
		case <-time.After(time.Second):
			t.Fatal("initialize missing")
		}
	}
	// Draining applies bounded JSONL backpressure while concurrent producers
	// exercise gorilla's single-writer constraint.
	drained := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, first.stdout); drained <- err }()
	var wg sync.WaitGroup
	writeErrors := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			frame, _ := json.Marshal(map[string]any{"id": i, "method": "thread/read", "params": map[string]any{}})
			_, err := first.stdin.Write(append(frame, '\n'))
			writeErrors <- err
		}(i)
	}
	wg.Wait()
	for i := 0; i < 24; i++ {
		if err := <-writeErrors; err != nil {
			t.Fatal(err)
		}
		select {
		case <-frames:
		case <-time.After(time.Second):
			t.Fatal("lost concurrent RPC frame")
		}
	}
	stopChildSession(first, nil)
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("first connection reader leaked")
	}
	if _, err := second.stdin.Write([]byte("{\"id\":25,\"method\":\"thread/read\"}\n")); err != nil {
		t.Fatalf("closing first wrapper affected external server: %v", err)
	}
	line, err := bufio.NewReader(second.stdout).ReadBytes('\n')
	if err != nil || !json.Valid(line) || strings.Count(string(line), "\n") != 1 {
		t.Fatalf("WS pretty JSON was not converted to JSONL: %q, %v", line, err)
	}
}

func TestSharedCodexSessionCancellationBoundsBootstrap(t *testing.T) {
	closed := make(chan struct{})
	url := sharedTestServer(t, func(conn *websocket.Conn) {
		defer close(closed)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := sharedTestApp(url).launchCodexChildSession(ctx, nil, nil); err == nil {
		t.Fatal("unanswered initialize accepted")
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancellation did not bound initialize")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cancellation leaked WS connection")
	}
}

func TestSharedCodexSessionDisconnectReportsUnknownBackendState(t *testing.T) {
	disconnect := make(chan struct{})
	url := sharedTestServer(t, func(conn *websocket.Conn) {
		if _, err := sharedBootstrapReply(conn); err == nil {
			<-disconnect
		}
	})
	session, err := sharedTestApp(url).launchCodexChildSession(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stopChildSession(session, nil)
	close(disconnect)
	_, err = io.ReadAll(session.stdout)
	if err == nil {
		t.Fatal("disconnect was mistaken for clean process EOF")
	}
	select {
	case err := <-session.waitErr:
		problem, ok := err.(agentproto.ErrorInfo)
		if !ok || problem.Code != "shared_backend_disconnected" {
			t.Fatalf("disconnect problem = %#v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("no transport failure received")
	}
}

func TestSharedCodexSessionRejectsChildOverridesBeforeConnecting(t *testing.T) {
	for _, args := range [][]string{{"-c", "model=x", "app-server"}, {"app-server", "-c", "model=x"}, {"app-server", "--listen", "ws://127.0.0.1:3"}, {"app-server", "--analytics-default-enabled"}, {"app-server", "daemon", "start"}} {
		app := sharedTestApp("ws://127.0.0.1:3")
		app.config.Args = args
		if _, err := app.launchCodexChildSession(context.Background(), nil, nil); err == nil || strings.Contains(err.Error(), "connection") {
			t.Fatalf("startup overrides not explicitly rejected: %#v, %v", args, err)
		}
	}
	for _, args := range [][]string{{"app-server"}, {"app-server", "--stdio"}, {"app-server", "--listen=stdio://"}, {"app-server", "--listen", "stdio://"}} {
		if err := validateSharedCodexArgs(Config{Args: args, Source: "headless"}); err != nil {
			t.Fatalf("valid mode flags rejected: %v", err)
		}
	}
	if err := validateSharedCodexArgs(Config{Args: []string{"app-server"}, Source: "headless", CodexProfileID: "custom"}); err == nil {
		t.Fatal("per-client profile silently ignored")
	}
}

func TestSharedCodexSessionRejectsNonHeadlessNativeParent(t *testing.T) {
	app := sharedTestApp("ws://127.0.0.1:3")
	app.config.Source = "vscode"
	if _, err := app.launchCodexChildSession(context.Background(), nil, nil); err == nil || !strings.Contains(err.Error(), "only supports headless/cron") {
		t.Fatalf("unsupported native parent did not fail explicitly: %v", err)
	}
}

func TestSharedCodexSessionAcceptsNormalizedNativeProfile(t *testing.T) {
	if err := validateSharedCodexArgs(Config{Args: []string{"app-server"}, Source: "headless", CodexProfileID: state.NativeCodexProfileID}); err != nil {
		t.Fatal(err)
	}
}

func TestSharedCodexSessionExitDoesNotReconcileBackendTurns(t *testing.T) {
	tracker := newRuntimeTurnTracker()
	tracker.ObserveEvents([]agentproto.Event{{Kind: agentproto.EventTurnStarted, ThreadID: "shared-thread", TurnID: "still-running"}})
	var reported []agentproto.ErrorInfo
	emitChildSessionExit(&childSession{sharedBackend: true}, nil, tracker, io.EOF, func(problem agentproto.ErrorInfo) { reported = append(reported, problem) })
	if len(reported) != 1 || reported[0].Code != "shared_backend_disconnected" {
		t.Fatalf("transport problem missing: %#v", reported)
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.turns["still-running"] == nil {
		t.Fatal("shared disconnect fabricated completion for a backend-owned active turn")
	}
}

type sharedTimeoutRuntime struct{ backendRuntime }

func (sharedTimeoutRuntime) CheckSharedBackendTimeout(time.Time) error { return io.ErrUnexpectedEOF }

func TestSharedCodexSessionOwnershipWatchdogClosesSilentConnection(t *testing.T) {
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
	app := sharedTestApp(url)
	app.runtime = sharedTimeoutRuntime{app.runtime}
	problems := make(chan agentproto.ErrorInfo, 1)
	session, err := app.launchCodexChildSession(context.Background(), nil, func(p agentproto.ErrorInfo) { problems <- p })
	if err != nil {
		t.Fatal(err)
	}
	defer stopChildSession(session, nil)
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("silent ownership deadline did not close connection")
	}
	select {
	case <-problems:
	case <-time.After(time.Second):
		t.Fatal("ownership timeout not reported")
	}
}

func TestSharedCodexSessionRestartReconnectsAndRestoresExactThread(t *testing.T) {
	resumes := make(chan map[string]any, 1)
	url := sharedTestServer(t, func(conn *websocket.Conn) {
		if _, err := sharedBootstrapReply(conn); err != nil {
			return
		}
		for {
			var frame map[string]any
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			resumes <- frame
			if err := conn.WriteJSON(map[string]any{"id": frame["id"], "result": map[string]any{"thread": map[string]any{"id": "exact-thread", "cwd": "C:/project"}}}); err != nil {
				return
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app := sharedTestApp(url)
	current, err := app.launchCodexChildSession(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.runtime.ObserveClient([]byte(`{"id":"old-resume","method":"thread/resume","params":{"threadId":"exact-thread","cwd":"C:/project"}}`)); err != nil {
		t.Fatal(err)
	}
	client := relayws.NewClient("ws://127.0.0.1:3", agentproto.Hello{}, relayws.ClientCallbacks{})
	defer client.Close()
	var generation int64
	writeCh, errCh := make(chan []byte, 8), make(chan error, 8)
	next, err := app.restartChildSession(ctx, restartRequest{CommandID: "reconnect"}, current, io.Discard, io.Discard, writeCh, client, newCommandResponseTracker(), newRuntimeTurnTracker(), &generation, 2, errCh, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stopChildSession(next, nil)
	if next == current || next.cmd != nil || !next.sharedBackend {
		t.Fatal("restart unexpectedly replaced an external backend process")
	}
	select {
	case frame := <-resumes:
		if frame["method"] != "thread/resume" || frame["params"].(map[string]any)["threadId"] != "exact-thread" || frame["params"].(map[string]any)["excludeTurns"] != true {
			t.Fatalf("reconnect replayed wrong operation: %#v", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("reconnect did not restore exact thread")
	}
	stopChildSession(next, nil)
	waitForSessionIOStopped(next, time.Second)
}

type sharedRejectRuntime struct{ backendRuntime }

func (sharedRejectRuntime) SharedBackend() bool { return true }
func (sharedRejectRuntime) ObserveClient([]byte) (runtimeObserveResult, error) {
	return runtimeObserveResult{}, io.ErrUnexpectedEOF
}
func (sharedRejectRuntime) ObserveServer([]byte) (runtimeObserveResult, error) {
	return runtimeObserveResult{}, io.ErrUnexpectedEOF
}

func TestSharedCodexSessionOwnershipErrorsCannotForwardOriginalFrame(t *testing.T) {
	runtime := sharedRejectRuntime{newBackendRuntime(Config{CodexAppServerURL: "ws://127.0.0.1:3"})}
	frame := "{\"id\":\"not-owned\",\"result\":{}}\n"
	writeCh, errCh := make(chan []byte, 1), make(chan error, 2)
	stdinLoop(context.Background(), strings.NewReader(frame), writeCh, runtime, nil, errCh, nil, nil, nil)
	select {
	case <-writeCh:
		t.Fatal("illegal native response forwarded to shared backend")
	default:
	}
	if err := <-errCh; err == nil {
		t.Fatal("client ownership error missing")
	}
	var parent bytes.Buffer
	done := make(chan struct{})
	stdoutLoop(context.Background(), strings.NewReader(frame), &parent, writeCh, runtime, nil, newCommandResponseTracker(), newRuntimeTurnTracker(), nil, 1, errCh, nil, nil, nil, done)
	if parent.Len() != 0 {
		t.Fatal("ownership failure forwarded an unclassified backend frame")
	}
	if err := <-errCh; err == nil {
		t.Fatal("server ownership error missing")
	}
}
