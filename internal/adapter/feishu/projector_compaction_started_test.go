package feishu

import (
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

func TestProjectContextCompactionStartedAndCompletedUseSameProgressCard(t *testing.T) {
	projector := NewProjector()
	for _, phase := range []struct {
		summary, status, messageID string
		kind                       OperationKind
	}{
		{"正在压缩上下文…", "running", "", OperationSendCard},
		{"上下文已压缩。", "completed", "om-compact-progress", OperationUpdateCard},
	} {
		progress := progressWithActiveSegment(control.ExecCommandProgress{
			ThreadID: "thread-1", TurnID: "turn-1", ItemID: "compact-1", Verbosity: "normal",
			Timeline: []control.ExecCommandProgressTimelineItem{timelineItem("compact-1", "context_compaction", "压缩", phase.summary, phase.status, 1)},
		}, phase.messageID, 1)
		ops := projector.ProjectEvent("chat-1", eventcontract.Event{Kind: eventcontract.KindExecCommandProgress, SurfaceSessionID: "surface-1", ExecCommandProgress: progress})
		if len(ops) != 1 {
			t.Fatalf("expected one progress operation, got %d", len(ops))
		}
		op := ops[0]
		if op.Kind != phase.kind || op.MessageID != phase.messageID || op.CardTitle != "工作中" {
			t.Fatalf("unexpected progress target: kind=%s message=%s", op.Kind, op.MessageID)
		}
		expected := "**压缩**：" + phase.summary
		if strings.Count(op.CardBody, expected) != 1 {
			t.Fatalf("missing or duplicate progress row: %q", op.CardBody)
		}
		payload := renderOperationCard(op, op.effectiveCardEnvelope())
		assertRenderedCardPayloadBasicInvariants(t, payload)
		body, _ := payload["body"].(map[string]any)
		elements, ok := cardPayloadElementsSlice(body["elements"])
		if !ok || len(elements) != 1 || markdownContent(elements[0]) != expected {
			t.Fatal("actual card JSON did not retain compaction row")
		}
	}
}
