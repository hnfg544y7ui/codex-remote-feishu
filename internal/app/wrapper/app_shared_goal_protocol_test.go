package wrapper

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestSharedCodexGoalControlAndAutomaticTurnTransport(t *testing.T) {
	serverErrors := make(chan error, 1)
	url := sharedTestServer(t, func(conn *websocket.Conn) {
		if _, err := sharedBootstrapReply(conn); err != nil {
			serverErrors <- err
			return
		}
		for _, operation := range []string{"set", "get", "clear"} {
			var frame map[string]any
			if err := conn.ReadJSON(&frame); err != nil {
				serverErrors <- err
				return
			}
			params, _ := frame["params"].(map[string]any)
			if frame["method"] != "thread/goal/"+operation || params["threadId"] != "goal-thread" {
				serverErrors <- fmt.Errorf("unexpected frame: %#v", frame)
				return
			}
			result := map[string]any{"goal": map[string]any{"threadId": "goal-thread", "objective": "isolated", "status": "paused"}}
			if operation == "clear" {
				result = map[string]any{"cleared": true}
			}
			if err := conn.WriteJSON(map[string]any{"id": frame["id"], "result": result}); err != nil {
				serverErrors <- err
				return
			}
		}
		for _, frame := range []string{
			`{"method":"thread/goal/updated","params":{"threadId":"goal-thread","goal":{"objective":"isolated","status":"active"}}}`,
			`{"method":"turn/started","params":{"threadId":"goal-thread","turn":{"id":"goal-auto"}}}`,
			`{"method":"turn/completed","params":{"threadId":"goal-thread","turn":{"id":"goal-auto","status":"completed"}}}`,
		} {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
				serverErrors <- err
				return
			}
		}
		serverErrors <- nil
		_, _, _ = conn.ReadMessage()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := sharedTestApp(url).launchCodexChildSession(ctx, nil, nil)
	if err != nil {
		select {
		case serverErr := <-serverErrors:
			t.Fatalf("launch: %v; server: %v", err, serverErr)
		default:
			t.Fatal(err)
		}
	}
	defer stopChildSession(session, nil)
	reader := bufio.NewReader(session.stdout)
	read := func() map[string]any {
		t.Helper()
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var frame map[string]any
		if err := json.Unmarshal(line, &frame); err != nil {
			t.Fatal(err)
		}
		return frame
	}
	for _, operation := range []string{"set", "get", "clear"} {
		frame, _ := json.Marshal(map[string]any{"id": operation, "method": "thread/goal/" + operation, "params": map[string]any{"threadId": "goal-thread", "status": "paused"}})
		if _, err := session.stdin.Write(append(frame, '\n')); err != nil {
			t.Fatal(err)
		}
		if response := read(); response["id"] != operation || response["result"] == nil {
			t.Fatalf("goal response lost: %#v", response)
		}
	}
	for _, method := range []string{"thread/goal/updated", "turn/started", "turn/completed"} {
		if notification := read(); notification["method"] != method {
			t.Fatalf("goal automatic turn notification lost: %#v", notification)
		}
	}
	if err := <-serverErrors; err != nil {
		t.Fatal(err)
	}
}
