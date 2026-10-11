package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu/cardtransport"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
)

func TestRenameLongNameAndErrorStayWithinCardBudget(t *testing.T) {
	gateway := &renameHookGateway{lifecycleGateway: newLifecycleGateway()}
	app := newRenameAppForTest(t, gateway)
	name := strings.Repeat("中文🙂", 10000)
	var captured agentproto.Command
	app.sendAgentCommand = func(_ string, command agentproto.Command) error { captured = command; return nil }
	app.handleUIEvents(context.Background(), app.service.ApplySurfaceAction(control.Action{
		Kind: control.ActionRenameCommand, SurfaceSessionID: "surface-1", Text: "/rename " + name,
	}))
	if captured.Name != name {
		t.Fatal("card display budget changed the persisted name request")
	}
	app.handleUIEvents(context.Background(), app.service.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind: agentproto.EventThreadNameCommandResult, CommandID: captured.CommandID, ThreadID: captured.Target.ThreadID,
		ErrorMessage: strings.Repeat("错误🙂", 10000),
	}))
	ops := gateway.snapshotOperations()
	if len(ops) != 2 {
		t.Fatalf("expected waiting and error card, got %d operations", len(ops))
	}
	for _, op := range ops {
		size, err := cardtransport.InteractiveMessageCardSize(op.CardTitle, op.CardTitleTag, "red", op.CardElements, true)
		if err != nil {
			t.Fatal(err)
		}
		if size > cardtransport.InteractiveCardTransportLimitBytes || len(op.CardElements) >= 200 {
			t.Fatalf("rename card exceeds transport budget: %d bytes, %d elements", size, len(op.CardElements))
		}
	}
}

func TestRenameFormLongOriginalNameStaysWithinCardBudget(t *testing.T) {
	gateway := &renameHookGateway{lifecycleGateway: newLifecycleGateway()}
	app := newRenameAppForTest(t, gateway)
	app.service.Instance("inst-1").Threads["inst-1-thread"].Name = strings.Repeat("中文🙂", 10000)
	result := handleGatewayActionForTest(context.Background(), app, control.Action{
		Kind: control.ActionRenameCommand, GatewayID: "app-1", SurfaceSessionID: "surface-1",
		ChatID: "chat-1", ActorUserID: "user-1", MessageID: "menu-owner", Text: "/rename", LocalPageAction: true,
		Inbound: &control.ActionInboundMeta{CardDaemonLifecycleID: app.daemonLifecycleID},
	})
	if result == nil || result.ReplaceCurrentCard == nil || findRenameFormSubmitPayload(result.ReplaceCurrentCard.CardElements) == nil {
		t.Fatalf("expected a rendered rename form callback: %#v", result)
	}
	op := result.ReplaceCurrentCard
	size, err := cardtransport.InteractiveMessageCardSize(op.CardTitle, op.CardTitleTag, "blue", op.CardElements, true)
	if err != nil || size > cardtransport.InteractiveCardTransportLimitBytes {
		t.Fatalf("rename form exceeds transport budget: %d bytes, %v", size, err)
	}
}
