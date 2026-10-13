package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/app/daemon/surfaceresume"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
)

type topicReceiptTestGateway struct {
	calls    int
	captured []feishu.Operation
	apply    func([]feishu.Operation) error
}

func (g *topicReceiptTestGateway) Start(context.Context, feishu.ActionHandler) error { return nil }
func (g *topicReceiptTestGateway) Apply(_ context.Context, ops []feishu.Operation) error {
	g.calls++
	g.captured = append(g.captured, ops...)
	if g.apply != nil {
		return g.apply(ops)
	}
	return nil
}

func topicAppCreatedEvents(t *testing.T, app *App, root string) (string, []eventcontract.Event) {
	t.Helper()
	events := app.service.ApplySurfaceAction(control.Action{Kind: control.ActionTopicCommand,
		SurfaceSessionID: "feishu:app-1:chat:oc_room", GatewayID: "app-1", ChatID: "oc_room", ActorUserID: "ou_owner",
		MessageID: root, Value: "new topic", Text: "/topic new topic"})
	surfaceID := feishuidentity.TopicRef("app-1", "oc_room", root).SurfaceID()
	if app.topicSurfaceLocked(surfaceID) == nil || len(events) != 1 {
		t.Fatalf("topic setup failed %#v", events)
	}
	return surfaceID, events
}

func TestTopicWelcomeDeliveryFreezesRootAndPersistsReceiptForRestart(t *testing.T) {
	stateDir := t.TempDir()
	app := newRestoreHintTestApp(stateDir)
	surfaceID, events := topicAppCreatedEvents(t, app, "om_root")
	gateway := &topicReceiptTestGateway{apply: func(ops []feishu.Operation) error {
		if len(ops) != 1 || ops[0].SurfaceSessionID != surfaceID || ops[0].ReplyToMessageID != "om_root" || !ops[0].ReplyInThread {
			t.Fatalf("topic delivery lost frozen route: %#v", ops)
		}
		ops[0].MessageID = "om_welcome"
		ops[0].FeishuTopicID = "omt_native"
		ops[0].FeishuTopicRootMessageID = "om_root"
		ops[0].FeishuTopicMessageURL = "https://example.invalid/topic"
		return nil
	}}
	app.gateway = gateway
	events[0].SourceMessageID = "om_other_source"
	app.mu.Lock()
	err := app.deliverUIEventLocked(t.Context(), events[0])
	app.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	store, err := surfaceresume.LoadStore(surfaceresume.StatePath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := store.Get(surfaceID)
	if !ok || entry.FeishuTopicID != "omt_native" || entry.FeishuTopicRootMessageID != "om_root" || entry.FeishuTopicMessageURL == "" {
		t.Fatalf("receipt not durable %#v", entry)
	}
	restored := newRestoreHintTestApp(stateDir)
	if got := restored.resolveTopicSurface("app-1", "oc_room", "omt_native", ""); got != surfaceID {
		t.Fatalf("native restart route=%s", got)
	}
	if got := restored.resolveTopicSurface("app-1", "oc_room", "", "om_root"); got != surfaceID {
		t.Fatalf("root restart route=%s", got)
	}
	if got := restored.lookupTopicMessageSurface("app-1", "om_welcome", "oc_room"); got != surfaceID {
		t.Fatalf("card ownership lost=%s", got)
	}
	for _, scope := range [][2]string{{"app-1", "oc_other"}, {"app-other", "oc_room"}} {
		if got := restored.lookupTopicMessageSurface(scope[0], "om_welcome", scope[1]); got != "" {
			t.Fatalf("forwarded callback found route=%s", got)
		}
	}
}

func TestTopicNativeReceiptRejectsChangedBindingWithoutPersistence(t *testing.T) {
	app := newRestoreHintTestApp(t.TempDir())
	surfaceID, _ := topicAppCreatedEvents(t, app, "om_root")
	if err := app.recordTopicDeliveryLocked(surfaceID, []feishu.Operation{{SurfaceSessionID: surfaceID, MessageID: "om_first", FeishuTopicID: "omt_a", FeishuTopicRootMessageID: "om_root"}}); err != nil {
		t.Fatal(err)
	}
	for _, op := range []feishu.Operation{
		{SurfaceSessionID: surfaceID, MessageID: "om_other", FeishuTopicID: "omt_b", FeishuTopicRootMessageID: "om_root"},
		{SurfaceSessionID: surfaceID, MessageID: "om_other", FeishuTopicID: "omt_a", FeishuTopicRootMessageID: "om_otherroot"},
		{SurfaceSessionID: "feishu:app-1:chat:oc_room", MessageID: "om_other", FeishuTopicID: "omt_a", FeishuTopicRootMessageID: "om_root"},
	} {
		if err := app.recordTopicDeliveryLocked(surfaceID, []feishu.Operation{op}); err == nil {
			t.Fatalf("accepted changed receipt %#v", op)
		}
	}
	if got := app.resolveTopicSurface("app-1", "oc_room", "omt_b", ""); got != "" {
		t.Fatalf("failed receipt became route %s", got)
	}
	if got := app.lookupTopicMessageSurface("app-1", "om_other", "oc_room"); got != "" {
		t.Fatalf("failed receipt became callback owner %s", got)
	}
}

func TestTopicPersistenceFailurePreventsUISend(t *testing.T) {
	app := newRestoreHintTestApp(t.TempDir())
	_, events := topicAppCreatedEvents(t, app, "om_root")
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	app.surfaceResumeRuntime.store = surfaceresume.NewStore(filepath.Join(blocker, "resume.json"))
	gateway := &topicReceiptTestGateway{}
	app.gateway = gateway
	app.mu.Lock()
	err := app.deliverUIEventLocked(t.Context(), events[0])
	app.mu.Unlock()
	if err == nil || gateway.calls != 0 {
		t.Fatalf("persist failure sent UI: err=%v calls=%d", err, gateway.calls)
	}
}

func TestTopicPersistenceFailurePreventsIngressDispatch(t *testing.T) {
	app := newRestoreHintTestApp(t.TempDir())
	gateway := &topicReceiptTestGateway{}
	app.gateway = gateway
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	app.surfaceResumeRuntime.store = surfaceresume.NewStore(filepath.Join(blocker, "resume.json"))
	app.handleAction(t.Context(), control.Action{Kind: control.ActionTopicCommand, SurfaceSessionID: "feishu:app-1:chat:oc_room",
		GatewayID: "app-1", ChatID: "oc_room", ActorUserID: "ou_owner", MessageID: "om_root", Value: "new topic", Text: "/topic new topic"})
	surfaceID := feishuidentity.TopicRef("app-1", "oc_room", "om_root").SurfaceID()
	surface := app.topicSurfaceLocked(surfaceID)
	if surface == nil || !surface.FeishuTopicClosed || gateway.calls != 0 {
		t.Fatalf("persist failure dispatched: surface=%#v calls=%d", surface, gateway.calls)
	}
	if len(app.service.ActiveRemoteTurns()) != 0 || len(app.service.PendingRemoteTurns()) != 0 {
		t.Fatal("persistence failure started a turn")
	}
}

func TestTopicRoutingIndexConflictingNativeAndRootFailsClosed(t *testing.T) {
	app := newRestoreHintTestApp(t.TempDir())
	surfaceA, _ := topicAppCreatedEvents(t, app, "om_a")
	surfaceB, _ := topicAppCreatedEvents(t, app, "om_b")
	for _, p := range []struct{ surface, root, native string }{{surfaceA, "om_a", "omt_a"}, {surfaceB, "om_b", "omt_b"}} {
		if err := app.recordTopicDeliveryLocked(p.surface, []feishu.Operation{{SurfaceSessionID: p.surface, MessageID: p.root + "_reply", FeishuTopicID: p.native, FeishuTopicRootMessageID: p.root}}); err != nil {
			t.Fatal(err)
		}
	}
	if got := app.resolveTopicSurface("app-1", "oc_room", "omt_b", "om_a"); got != "" {
		t.Fatalf("conflicting metadata resolved=%s", got)
	}
	if got := app.resolveTopicSurface("app-1", "oc_other", "omt_a", "om_a"); got != "" {
		t.Fatalf("crosschat root resolved=%s", got)
	}
}

func TestTopicStableCardUpdateKeepsNativeBinding(t *testing.T) {
	app := newRestoreHintTestApp(t.TempDir())
	surfaceID, _ := topicAppCreatedEvents(t, app, "om_root")
	if err := app.recordTopicDeliveryLocked(surfaceID, []feishu.Operation{{SurfaceSessionID: surfaceID, MessageID: "om_existing", FeishuTopicID: "omt_a", FeishuTopicRootMessageID: "om_root"}}); err != nil {
		t.Fatal(err)
	}
	gateway := &topicReceiptTestGateway{apply: func(ops []feishu.Operation) error {
		if len(ops) != 1 || ops[0].Kind != feishu.OperationUpdateCard || ops[0].MessageID != "om_existing" || ops[0].ReplyInThread || ops[0].FeishuTopicID != "omt_a" {
			t.Fatalf("stable topic update changed operation: %#v", ops)
		}
		return nil
	}}
	app.gateway = gateway
	event := eventcontract.Event{Kind: eventcontract.KindPage, SurfaceSessionID: surfaceID, PageView: &control.FeishuPageView{MessageID: "om_existing", Title: "History"}}
	app.mu.Lock()
	err := app.deliverUIEventLocked(t.Context(), event)
	app.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if app.topicSurfaceLocked(surfaceID).FeishuTopicID != "omt_a" || gateway.calls != 1 {
		t.Fatal("update changed native binding")
	}
}

func TestTopicReceiptBatchRejectsBeforeMutatingSurface(t *testing.T) {
	app := newRestoreHintTestApp(t.TempDir())
	surfaceID, _ := topicAppCreatedEvents(t, app, "om_root")
	err := app.recordTopicDeliveryLocked(surfaceID, []feishu.Operation{
		{SurfaceSessionID: surfaceID, MessageID: "om_first", FeishuTopicID: "omt_a", FeishuTopicRootMessageID: "om_root"},
		{SurfaceSessionID: surfaceID, MessageID: "om_bad", FeishuTopicID: "omt_b", FeishuTopicRootMessageID: "om_other"},
	})
	surface := app.topicSurfaceLocked(surfaceID)
	if err == nil {
		t.Fatal("invalid receipt batch accepted")
	}
	if surface.FeishuTopicID != "" || len(surface.FeishuTopicMessageIDs) != 1 || surface.FeishuTopicMessageIDs[0] != "om_root" {
		t.Fatalf("invalid receipt batch partially mutated surface: %#v", surface)
	}
}

func TestUnrelatedLegacyIngressDoesNotPersistBrokenTopic(t *testing.T) {
	app := newRestoreHintTestApp(t.TempDir())
	_, _ = topicAppCreatedEvents(t, app, "om_root")
	app.surfaceResumeRuntime.status = persistedStoreStatusDegraded
	gateway := &topicReceiptTestGateway{}
	app.gateway = gateway
	app.handleAction(t.Context(), control.Action{Kind: control.ActionStatus, SurfaceSessionID: "feishu:app-1:user:ou_private", GatewayID: "app-1", ActorUserID: "ou_private", MessageID: "om_status"})
	if gateway.calls == 0 {
		t.Fatal("unrelated legacy status blocked by topic persistence")
	}
}

type topicMediaTestSender struct {
	request feishu.IMFileSendRequest
	result  feishu.IMFileSendResult
	send    func(feishu.IMFileSendRequest)
}

func (s *topicMediaTestSender) SendIMFile(_ context.Context, req feishu.IMFileSendRequest) (feishu.IMFileSendResult, error) {
	s.request = req
	if s.send != nil {
		s.send(req)
	}
	return s.result, nil
}

func TestTopicDirectMediaOwnershipSurvivesRestartAndUsesCapturedSurface(t *testing.T) {
	stateDir := t.TempDir()
	app := newRestoreHintTestApp(stateDir)
	surfaceA, _ := topicAppCreatedEvents(t, app, "om_a")
	surfaceB, _ := topicAppCreatedEvents(t, app, "om_b")
	for _, p := range []struct{ surface, root, native string }{{surfaceA, "om_a", "omt_a"}, {surfaceB, "om_b", "omt_b"}} {
		if err := app.recordTopicDeliveryLocked(p.surface, []feishu.Operation{{SurfaceSessionID: p.surface, MessageID: p.root + "_welcome", FeishuTopicID: p.native, FeishuTopicRootMessageID: p.root}}); err != nil {
			t.Fatal(err)
		}
	}
	sender := &topicMediaTestSender{result: feishu.IMFileSendResult{SurfaceSessionID: surfaceB, MessageID: "om_file", FileName: "file.txt"}}
	prepared := preparedSendIMFile{sender: sender, request: feishu.IMFileSendRequest{SurfaceSessionID: surfaceA, GatewayID: "app-1", ChatID: "oc_room", Path: "file.txt"}}
	app.mu.Lock()
	events := app.runPreparedSendIMFileLocked(control.DaemonCommand{SurfaceSessionID: surfaceA}, prepared)
	app.mu.Unlock()
	if len(events) != 1 || events[0].Notice == nil || events[0].Notice.Code != "send_file_sent" {
		t.Fatalf("file send failed %#v", events)
	}
	if sender.request.SurfaceSessionID != surfaceA {
		t.Fatal("request retargeted")
	}
	// Even if a sender result supplies an unrelated display/routing field, ownership
	// follows the request captured before the network call, never the current selection.
	if got := app.lookupTopicMessageSurface("app-1", "om_file", "oc_room"); got != surfaceA {
		t.Fatalf("receipt not captured to original surface %s", got)
	}
	if err := app.recordTopicMediaDelivery(surfaceA, "om_image"); err != nil {
		t.Fatal(err)
	}
	if err := app.recordTopicMediaDelivery(surfaceA, "om_video"); err != nil {
		t.Fatal(err)
	}
	restored := newRestoreHintTestApp(stateDir)
	for _, id := range []string{"om_file", "om_image", "om_video"} {
		if got := restored.lookupTopicMessageSurface("app-1", id, "oc_room"); got != surfaceA {
			t.Fatalf("media ownership lost after restart: id=%s surface=%s", id, got)
		}
		if got := restored.lookupTopicMessageSurface("app-1", id, "oc_other"); got != "" {
			t.Fatalf("crosschat media ownership exposed: %s", got)
		}
	}
	if topic := restored.topicSurfaceLocked(surfaceB); len(topic.FeishuTopicMessageIDs) != 2 {
		t.Fatalf("other topic received media receipt %#v", topic.FeishuTopicMessageIDs)
	}
}

func TestTopicDirectMediaMissingReceiptIsRefused(t *testing.T) {
	app := newRestoreHintTestApp(t.TempDir())
	surfaceID, _ := topicAppCreatedEvents(t, app, "om_root")
	if err := app.recordTopicMediaDelivery(surfaceID, "om_file"); err == nil {
		t.Fatal("pending native topic accepted media receipt")
	}
	if err := app.recordTopicDeliveryLocked(surfaceID, []feishu.Operation{{SurfaceSessionID: surfaceID, MessageID: "om_welcome", FeishuTopicID: "omt_native", FeishuTopicRootMessageID: "om_root"}}); err != nil {
		t.Fatal(err)
	}
	sender := &topicMediaTestSender{result: feishu.IMFileSendResult{FileName: "file.txt"}}
	app.mu.Lock()
	events := app.runPreparedSendIMFileLocked(control.DaemonCommand{SurfaceSessionID: surfaceID}, preparedSendIMFile{sender: sender, request: feishu.IMFileSendRequest{SurfaceSessionID: surfaceID, GatewayID: "app-1", ChatID: "oc_room", Path: "file.txt"}})
	app.mu.Unlock()
	if len(events) != 1 || events[0].Notice == nil || events[0].Notice.Code == "send_file_sent" {
		t.Fatalf("missing message receipt reported success %#v", events)
	}
	for _, id := range []string{"", " ", "om_file"} {
		if got := app.lookupTopicMessageSurface("app-1", id, "oc_room"); got != "" {
			t.Fatalf("missing media receipt registered ownership=%s", got)
		}
	}
}
