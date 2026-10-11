package wrapper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kxn/codex-remote-feishu/internal/adapter/relayws"
	"github.com/kxn/codex-remote-feishu/internal/app/appserverendpoint"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/debuglog"
)

const sharedBackendFrameLimit = 16 << 20

// sharedCodexTransport owns a connection, never the external server process.
// The pipe retains the existing JSONL bootstrap/translation path and applies
// backpressure instead of buffering an unbounded stream of notifications.
type sharedCodexTransport struct {
	conn      *websocket.Conn
	reader    *io.PipeReader
	writer    *io.PipeWriter
	writeMu   sync.Mutex
	closeOnce sync.Once
	done      chan struct{}
	waitErr   chan error
}

func validateSharedCodexArgs(cfg Config) error {
	if !(&App{config: cfg}).needsSyntheticBootstrap() {
		return fmt.Errorf("shared Codex backend only supports headless/cron wrappers; desktop must connect to the WebSocket endpoint directly")
	}
	if state.NormalizeCodexProfileID(cfg.CodexProfileID) != state.NativeCodexProfileID {
		return fmt.Errorf("shared Codex backend cannot apply a per-wrapper Codex profile; configure the external backend instead")
	}
	if len(cfg.Args) == 0 || cfg.Args[0] != "app-server" {
		return fmt.Errorf("shared Codex backend requires app-server without child startup overrides")
	}
	for i := 1; i < len(cfg.Args); i++ {
		switch cfg.Args[i] {
		case "--stdio", "--listen=stdio://":
		case "--listen":
			if i+1 >= len(cfg.Args) || cfg.Args[i+1] != "stdio://" {
				return fmt.Errorf("shared Codex URL conflicts with child listener arguments")
			}
			i++
		default:
			return fmt.Errorf("shared Codex backend cannot apply child startup overrides; configure the external backend instead")
		}
	}
	return nil
}

func (a *App) launchSharedCodexSession(ctx context.Context, rawLogger *debuglog.RawLogger, reportProblem func(agentproto.ErrorInfo)) (*childSession, error) {
	endpoint, err := appserverendpoint.NormalizeURL(a.config.CodexAppServerURL)
	if err != nil {
		return nil, err
	}
	if err := validateSharedCodexArgs(a.config); err != nil {
		return nil, err
	}
	// No HTTP_PROXY lookup: a local privileged backend must always be reached
	// directly. Redirects are rejected by gorilla's WebSocket handshake.
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, response, err := dialer.DialContext(ctx, endpoint, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, sharedBackendTransportProblem(err)
	}
	conn.SetReadLimit(sharedBackendFrameLimit)
	transport := newSharedCodexTransport(ctx, conn)
	go a.watchSharedCodexOwnership(ctx, transport, reportProblem)
	stdout, err := a.runChildBootstrap(ctx, wrapperBootstrapTimeout, func() { _ = transport.Close() }, func() (io.Reader, error) {
		initialized, err := a.bootstrapHeadlessCodex(transport, transport.reader, rawLogger, reportProblem)
		if err != nil {
			return nil, err
		}
		return a.bootstrapSharedResume(transport, initialized, rawLogger, reportProblem)
	})
	if err != nil {
		_ = transport.Close()
		if a.config.ResumeThreadID != "" {
			return nil, sharedBootstrapScopeProblem(err, a.config.ResumeThreadID)
		}
		return nil, err
	}
	return &childSession{
		sharedBackend: true,
		stdin:         transport, stdout: stdout, stderr: bytes.NewReader(nil),
		stdoutClose: transport.reader, waitErr: transport.waitErr,
		cancel: func() { _ = transport.Close() },
	}, nil
}

func newSharedCodexTransport(ctx context.Context, conn *websocket.Conn) *sharedCodexTransport {
	conn.SetReadLimit(sharedBackendFrameLimit)
	reader, writer := io.Pipe()
	t := &sharedCodexTransport{conn: conn, reader: reader, writer: writer, done: make(chan struct{}), waitErr: make(chan error, 1)}
	go t.readFrames()
	go func() {
		select {
		case <-ctx.Done():
			_ = t.Close()
		case <-t.done:
		}
	}()
	return t
}

func (t *sharedCodexTransport) readFrames() {
	var terminal error
	defer func() {
		problem := sharedBackendTransportProblem(terminal)
		_ = t.writer.CloseWithError(problem)
		_ = t.conn.Close()
		t.waitErr <- problem
		close(t.waitErr)
		close(t.done)
	}()
	for {
		kind, payload, err := t.conn.ReadMessage()
		if err != nil {
			terminal = err
			return
		}
		if kind != websocket.TextMessage {
			terminal = fmt.Errorf("shared Codex backend sent a non-text RPC frame")
			return
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, payload); err != nil || compact.Len() == 0 || compact.Bytes()[0] != '{' {
			terminal = fmt.Errorf("shared Codex backend sent an invalid JSON-RPC object")
			return
		}
		if _, err := t.writer.Write(append(compact.Bytes(), '\n')); err != nil {
			terminal = err
			return
		}
	}
}

func (t *sharedCodexTransport) Write(frame []byte) (int, error) {
	if len(frame) > sharedBackendFrameLimit {
		return 0, fmt.Errorf("shared Codex RPC frame exceeds %d bytes", sharedBackendFrameLimit)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, frame); err != nil || compact.Len() == 0 || compact.Bytes()[0] != '{' {
		return 0, fmt.Errorf("shared Codex RPC write requires one JSON object")
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if err := t.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return 0, sharedBackendTransportProblem(err)
	}
	if err := t.conn.WriteMessage(websocket.TextMessage, compact.Bytes()); err != nil {
		return 0, sharedBackendTransportProblem(err)
	}
	return len(frame), nil
}

func (t *sharedCodexTransport) Close() error {
	t.closeOnce.Do(func() {
		_ = t.conn.Close()
		_ = t.reader.Close()
		_ = t.writer.Close()
	})
	return nil
}

func sharedBackendTransportProblem(err error) agentproto.ErrorInfo {
	details := "connection closed"
	if err != nil {
		details = err.Error()
	}
	return agentproto.ErrorInfo{
		Code: "shared_backend_disconnected", Layer: "wrapper", Stage: "shared_backend_transport",
		Operation: "codex.websocket", Message: "共享 Codex 后端连接已断开；后端任务状态未知。",
		Details: details, Retryable: true,
	}
}

func emitChildSessionExit(session *childSession, client *relayws.Client, tracker *runtimeTurnTracker, err error, reportProblem func(agentproto.ErrorInfo)) {
	if session != nil && session.sharedBackend {
		if reportProblem != nil {
			reportProblem(sharedBackendTransportProblem(err))
		}
		return
	}
	emitRuntimeExitReconciliation(client, tracker, err, reportProblem)
}

func (a *App) watchSharedCodexOwnership(ctx context.Context, transport *sharedCodexTransport, reportProblem func(agentproto.ErrorInfo)) {
	checker, ok := a.runtime.(interface{ CheckSharedBackendTimeout(time.Time) error })
	if !ok {
		return
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-transport.done:
			return
		case now := <-ticker.C:
			if err := checker.CheckSharedBackendTimeout(now); err != nil {
				if reportProblem != nil {
					reportProblem(agentproto.ErrorInfoFromError(err, sharedBackendTransportProblem(err)))
				}
				_ = transport.Close()
				return
			}
		}
	}
}
