package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/adapter/codex"
	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestContextCompactionStartAndCompletionPatchSameProgressCard(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gateway := newLifecycleGateway()
	app := New(":0", ":0", gateway, agentproto.ServerIdentity{})
	app.service.UpsertInstance(&state.InstanceRecord{
		InstanceID: "inst-compact", WorkspaceRoot: workspace, WorkspaceKey: workspace, Online: true,
		ObservedFocusedThreadID: "thread-compact",
		Threads: map[string]*state.ThreadRecord{
			"thread-compact": {ThreadID: "thread-compact", CWD: workspace, Loaded: true},
		},
	})
	app.service.MaterializeSurface("surface-compact", "app-compact", "chat-compact", "user-compact")
	app.service.ApplySurfaceAction(control.Action{
		Kind: control.ActionAttachInstance, SurfaceSessionID: "surface-compact", GatewayID: "app-compact",
		ChatID: "chat-compact", ActorUserID: "user-compact", InstanceID: "inst-compact",
	})
	for _, surface := range app.service.Surfaces() {
		surface.Verbosity = state.SurfaceVerbosityNormal
	}
	translator := codex.NewTranslator("inst-compact")
	started := []byte(`{"method":"item/started","params":{"threadId":"thread-compact","turnId":"turn-compact","item":{"id":"compact-1","type":"contextCompaction"}}}`)
	completed := []byte(`{"method":"item/completed","params":{"threadId":"thread-compact","turnId":"turn-compact","item":{"id":"compact-1","type":"contextCompaction"}}}`)
	apply := func(frame []byte) {
		t.Helper()
		result, err := translator.ObserveServer(frame)
		if err != nil || len(result.Events) != 1 {
			t.Fatalf("native compaction frame: events=%d err=%v", len(result.Events), err)
		}
		app.handleUIEvents(context.Background(), app.service.ApplyAgentEvent("inst-compact", result.Events[0]))
	}
	apply(started)
	operations := gateway.snapshotOperations()
	if len(operations) != 1 || operations[0].Kind != feishu.OperationSendCard || operations[0].MessageID == "" {
		t.Fatalf("start must send one tracked progress card: operations=%d", len(operations))
	}
	start := operations[0]
	startText := start.CardBody + strings.Join(cardMarkdownContents(start.CardElements), "\n")
	if !strings.Contains(startText, "正在压缩上下文…") || strings.Contains(startText, "上下文已压缩") {
		t.Fatalf("start card did not render the true starting state: %q", startText)
	}
	apply(completed)
	operations = gateway.snapshotOperations()
	if len(operations) != 2 || operations[1].Kind != feishu.OperationUpdateCard || operations[1].MessageID != start.MessageID {
		t.Fatalf("completion must patch the initial progress card: operations=%d", len(operations))
	}
	finish := operations[1]
	finishText := finish.CardBody + strings.Join(cardMarkdownContents(finish.CardElements), "\n")
	if !strings.Contains(finishText, "上下文已压缩。") || strings.Contains(finishText, "正在压缩上下文") {
		t.Fatalf("completion must replace the same row: %q", finishText)
	}
	apply(started)
	if len(gateway.snapshotOperations()) != 2 {
		t.Fatal("late duplicate start reopened the completed progress row")
	}
}
