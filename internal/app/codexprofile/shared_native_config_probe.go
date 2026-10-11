package codexprofile

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kxn/codex-remote-feishu/internal/app/appserverendpoint"
)

const sharedNativeConfigProbeFrameLimit = 1 << 20

// runSharedNativeConfigProbe observes the selected server rather than spawning
// a private CLI with potentially different configuration. It owns only this
// read-only connection; closing it never terminates the shared server process.
func runSharedNativeConfigProbe(ctx context.Context, options NativeConfigProbeOptions) (NativeConfigObservation, error) {
	endpoint, err := appserverendpoint.NormalizeURL(options.ServerURL)
	if err != nil || endpoint == "" {
		return NativeConfigObservation{}, nativeConfigProbeError("shared_url")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Explicit loopback must not be routed through an ambient system proxy.
	dialer := websocket.Dialer{Proxy: nil, HandshakeTimeout: 10 * time.Second}
	conn, response, err := dialer.DialContext(probeCtx, endpoint, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return NativeConfigObservation{}, nativeConfigProbeError("shared_connect")
	}
	defer conn.Close()
	conn.SetReadLimit(sharedNativeConfigProbeFrameLimit)
	if deadline, ok := probeCtx.Deadline(); ok {
		_ = conn.SetReadDeadline(deadline)
		_ = conn.SetWriteDeadline(deadline)
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-probeCtx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	stream := &sharedNativeConfigProbeStream{conn: conn}
	return RunNativeConfigProbeSession(probeCtx, stream, stream, options.Version, "")
}

// This bounded adapter presents one WebSocket JSON object per JSONL line to
// the existing native session, including responses formatted across lines.
type sharedNativeConfigProbeStream struct {
	conn    *websocket.Conn
	pending []byte
}

func (s *sharedNativeConfigProbeStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(s.pending) == 0 {
		kind, raw, err := s.conn.ReadMessage()
		if err != nil {
			return 0, io.EOF
		}
		var compact bytes.Buffer
		if kind != websocket.TextMessage || json.Compact(&compact, raw) != nil || compact.Len() == 0 || compact.Bytes()[0] != '{' {
			return 0, nativeConfigProbeError("shared_frame")
		}
		s.pending = append(compact.Bytes(), '\n')
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

func (s *sharedNativeConfigProbeStream) Write(p []byte) (int, error) {
	if len(p) > sharedNativeConfigProbeFrameLimit {
		return 0, nativeConfigProbeError("shared_write_size")
	}
	if err := s.conn.WriteMessage(websocket.TextMessage, bytes.TrimSpace(p)); err != nil {
		return 0, nativeConfigProbeError("shared_write")
	}
	return len(p), nil
}
