package appserverendpoint

import "testing"

func TestNormalizeURL(t *testing.T) {
	for _, raw := range []string{"", "ws://127.0.0.1:45000", " ws://127.0.0.1:45000/ ", "ws://[::1]:1"} {
		if _, err := NormalizeURL(raw); err != nil {
			t.Fatalf("valid URL %q: %v", raw, err)
		}
	}
	for _, raw := range []string{"ws://localhost:3", "ws://0.0.0.0:3", "ws://127.1:3", "wss://127.0.0.1:3", "ws://127.0.0.1", "ws://127.0.0.1:0", "ws://127.0.0.1:65536", "ws://127.0.0.1:03", "ws://u:p@127.0.0.1:3", "ws://127.0.0.1:3/rpc", "ws://127.0.0.1:3?", "ws://127.0.0.1:3#", "ws://127.0.0.1:3/%2f", "ws://[::ffff:127.0.0.1]:3"} {
		if _, err := NormalizeURL(raw); err == nil {
			t.Errorf("unsafe URL %q accepted", raw)
		}
	}
}
