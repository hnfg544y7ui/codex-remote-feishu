package codex

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestObserveServerContextCompactionStartedPreservesIdentity(t *testing.T) {
	tr := NewTranslator("inst-1")
	result, err := tr.ObserveServer([]byte(`{"method":"item/started","params":{"threadId":"thread-1","turnId":"turn-1","item":{"id":"compact-1","type":"contextCompaction"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events=%d", len(result.Events))
	}
	e := result.Events[0]
	if e.Kind != agentproto.EventItemStarted || e.ItemKind != "context_compaction" || e.ThreadID != "thread-1" || e.TurnID != "turn-1" || e.ItemID != "compact-1" {
		t.Fatalf("unexpected start event: %#v", e)
	}
}
