package wrapper

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestSharedCodexTransportClassifiesCloseBeforeReply(t *testing.T) {
	for _, code := range []int{websocket.CloseTryAgainLater, websocket.CloseInternalServerErr} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			ready := make(chan struct{})
			observed := make(chan bool, 1)
			url := sharedTestServer(t, func(conn *websocket.Conn) {
				<-ready
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, "refusal"), time.Now().Add(time.Second))
				_ = conn.SetReadDeadline(time.Now().Add(time.Second))
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
				}
			})
			conn, _, err := (&websocket.Dialer{}).Dial(url, nil)
			if err != nil {
				t.Fatal(err)
			}
			defaultHandler := conn.CloseHandler()
			var transport *sharedCodexTransport
			conn.SetCloseHandler(func(receivedCode int, text string) error {
				<-ready
				// Observe classification while ReadMessage is still processing
				// the close frame, before the default reply can fail a writer.
				observed <- transport.maintenanceClosed.Load()
				return defaultHandler(receivedCode, text)
			})
			transport = newSharedCodexTransport(context.Background(), conn)
			defer transport.Close()
			close(ready)
			select {
			case maintenance := <-observed:
				if maintenance != (code == websocket.CloseTryAgainLater) {
					t.Fatal("close classification was late or marked a non-maintenance refusal retryable")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("close handshake did not reach the reply handler")
			}
		})
	}
}
