package wrapper

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/debuglog"
)

// Retry only explicit pre-subscription maintenance refusals. Once initialize
// succeeds, the caller sends thread/resume exactly once and fails closed on
// an unconfirmed subscription. All attempts share one bootstrap deadline.
func (a *App) initializeSharedCodex(sessionCtx, bootstrapCtx context.Context, endpoint string, rawLogger *debuglog.RawLogger, reportProblem func(agentproto.ErrorInfo)) (*sharedCodexTransport, io.Reader, error) {
	// No proxy or redirect lookup for the privileged local backend.
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	for {
		conn, response, err := dialer.DialContext(bootstrapCtx, endpoint, nil)
		temporary := response != nil && response.StatusCode == http.StatusServiceUnavailable
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if err != nil {
			if !temporary {
				return nil, nil, sharedBackendTransportProblem(err)
			}
		} else {
			transport := newSharedCodexTransport(sessionCtx, conn)
			stdout, bootstrapErr := a.runChildBootstrap(bootstrapCtx, wrapperBootstrapTimeout, func() { _ = transport.Close() }, func() (io.Reader, error) {
				return a.bootstrapHeadlessCodex(transport, transport.reader, rawLogger, reportProblem)
			})
			if bootstrapErr == nil {
				return transport, stdout, nil
			}
			temporary = transport.maintenanceClosed.Load()
			_ = transport.Close()
			if !temporary {
				return nil, nil, bootstrapErr
			}
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-bootstrapCtx.Done():
			timer.Stop()
			return nil, nil, bootstrapCtx.Err()
		case <-timer.C:
		}
	}
}
