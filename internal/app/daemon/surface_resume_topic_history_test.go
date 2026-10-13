package daemon

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/app/daemon/surfaceresume"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
	relayruntime "github.com/kxn/codex-remote-feishu/internal/runtime"
)

type topicHistoryRecoveryFixture struct {
	app       *App
	action    control.Action
	workspace string
	starts    int
	commands  []agentproto.Command
	gateway   *topicReceiptTestGateway
}

func newTopicHistoryRecoveryFixture(t *testing.T) *topicHistoryRecoveryFixture {
	t.Helper()
	stateDir, workspace := t.TempDir(), t.TempDir()
	id := feishuidentity.TopicRef("app-1", "oc_room", "om_root").SurfaceID()
	putSurfaceResumeStateForTest(t, stateDir, surfaceresume.Entry{SurfaceSessionID: id,
		GatewayID: "app-1", ChatID: "oc_room", ActorUserID: "ou_owner", ProductMode: "normal", Backend: "codex",
		FeishuTopicID: "omt_native", FeishuTopicRootMessageID: "om_root", FeishuTopicOwnerID: "ou_owner",
		FeishuTopicBoundThreadID: "thread-1", FeishuTopicMessageIDs: []string{"om_root", "om_menu"},
		ResumeThreadID: "thread-1", ResumeThreadCWD: workspace, ResumeWorkspaceKey: workspace, ResumeRouteMode: "pinned", ResumeHeadless: true})
	fixture := &topicHistoryRecoveryFixture{workspace: workspace, app: newRestoreHintTestApp(stateDir)}
	t.Cleanup(func() { _ = fixture.app.finishShutdown() })
	fixture.app.startHeadless = func(relayruntime.HeadlessLaunchOptions) (int, error) { fixture.starts++; return 4321, nil }
	fixture.app.sendAgentCommand = func(_ string, command agentproto.Command) error {
		fixture.commands = append(fixture.commands, command)
		return nil
	}
	fixture.gateway = &topicReceiptTestGateway{apply: func(ops []feishu.Operation) error {
		for i := range ops {
			if ops[i].MessageID == "" {
				ops[i].MessageID = fmt.Sprintf("om_delivery_%d_%d", fixture.gateway.calls, i)
			}
			ops[i].FeishuTopicID, ops[i].FeishuTopicRootMessageID = "omt_native", "om_root"
		}
		return nil
	}}
	fixture.app.gateway = fixture.gateway
	fixture.action = control.Action{Kind: control.ActionShowHistory, SurfaceSessionID: id, GatewayID: "app-1", ChatID: "oc_room",
		ActorUserID: "ou_owner", MessageID: "om_history", FeishuTopicID: "omt_native", FeishuTopicRootMessageID: "om_root", Text: "/history"}
	return fixture
}

func TestTopicColdHistoryRestoresThenReadsWithoutSendingPrompt(t *testing.T) {
	for _, card := range []bool{false, true} {
		t.Run(fmt.Sprintf("card=%t", card), func(t *testing.T) {
			f := newTopicHistoryRecoveryFixture(t)
			if card {
				f.action.MessageID = "om_menu"
				f.action.Inbound = &control.ActionInboundMeta{CardDaemonLifecycleID: f.app.daemonLifecycleID}
				f.action.CatalogFamilyID, f.action.CatalogBackend = control.FeishuCommandHistory, agentproto.BackendCodex
				f.action.CatalogVariantID = control.FeishuCommandVariantIDForContext(control.FeishuCommandHistory, control.CatalogContext{Backend: agentproto.BackendCodex, ProductMode: "normal"})
			}
			// Another running task remains untouched throughout the history read.
			f.app.service.UpsertInstance(&state.InstanceRecord{InstanceID: "busy-other", Online: true, WorkspaceRoot: t.TempDir(),
				ActiveTurnID: "running-turn", ActiveThreadID: "busy-thread"})
			f.app.HandleGatewayAction(t.Context(), f.action)
			pending := f.app.service.SurfaceSnapshot(f.action.SurfaceSessionID).PendingHeadless
			if f.starts != 1 || pending.InstanceID == "" {
				t.Fatal("cold history did not start one restore")
			}
			continuation := f.app.surfaceResumeRuntime.groupOnDemandContinuations[f.action.SurfaceSessionID]
			if continuation == nil || continuation.Action.Kind != control.ActionShowHistory || continuation.Action.IsCardAction() != card {
				t.Fatal("history action was not preserved")
			}
			connectGroupOnDemandPendingForTest(f.app, pending.InstanceID, f.workspace)
			var history agentproto.Command
			reads := 0
			for _, command := range f.commands {
				if command.Kind == agentproto.CommandPromptSend || strings.Contains(string(command.Kind), "goal") {
					t.Fatal("read-only history resumed task execution")
				}
				if command.Kind == agentproto.CommandThreadHistoryRead {
					reads++
					history = command
				}
			}
			if reads != 1 || history.Target.ThreadID != "thread-1" {
				texts := []string{}
				for _, op := range f.gateway.captured {
					texts = append(texts, operationCardText(op))
				}
				t.Fatalf("history reads=%d cards=%q", reads, texts)
			}
			if f.app.service.Instance("busy-other").ActiveTurnID != "running-turn" {
				t.Fatal("history recovery interrupted another task")
			}
			loading := false
			for _, op := range f.gateway.captured {
				if strings.Contains(operationCardText(op), "正在读取当前会话历史") {
					loading = true
				}
			}
			if !loading {
				t.Fatal("asynchronous replay dropped the loading history card")
			}
			f.app.onEvents(t.Context(), pending.InstanceID, []agentproto.Event{{Kind: agentproto.EventThreadHistoryRead, CommandID: history.CommandID,
				ThreadID: "thread-1", ThreadHistory: &agentproto.ThreadHistoryRecord{Thread: agentproto.ThreadSnapshotRecord{ThreadID: "thread-1"},
					Turns: []agentproto.ThreadHistoryTurnRecord{{TurnID: "turn-1", Status: "completed"}}}}})
			if got := f.app.service.SurfaceThreadHistory(f.action.SurfaceSessionID); got == nil || got.Thread.ThreadID != "thread-1" {
				t.Fatal("history result not recorded")
			}
			if f.app.surfaceResumeRuntime.groupOnDemandContinuations[f.action.SurfaceSessionID] != nil {
				t.Fatal("completed replay remained pending")
			}
		})
	}
}

func TestTopicColdHistoryRejectsUnauthorizedAndStaleRecovery(t *testing.T) {
	for _, reason := range []string{"owner", "closed", "native", "root", "chat", "binding", "unbound", "old-card", "old-command", "old-page", "old-detail"} {
		t.Run(reason, func(t *testing.T) {
			f := newTopicHistoryRecoveryFixture(t)
			surface := f.app.topicSurfaceLocked(f.action.SurfaceSessionID)
			switch reason {
			case "owner":
				f.action.ActorUserID = "other"
			case "closed":
				surface.FeishuTopicClosed = true
			case "native":
				f.action.FeishuTopicID = "omt_other"
			case "root":
				f.action.FeishuTopicRootMessageID = "om_other"
			case "chat":
				f.action.ChatID = "oc_other"
			case "binding":
				surface.FeishuTopicBoundThreadID = "other-thread"
			case "unbound":
				surface.FeishuTopicBoundThreadID = ""
			case "old-card":
				f.action.Inbound = &control.ActionInboundMeta{CardDaemonLifecycleID: "prior-daemon"}
			case "old-command":
				f.action.Inbound = &control.ActionInboundMeta{CardDaemonLifecycleID: f.app.daemonLifecycleID}
				f.action.CatalogFamilyID, f.action.CatalogBackend, f.action.CatalogVariantID = control.FeishuCommandHistory, agentproto.BackendClaude, "history.claude.normal"
			case "old-page":
				f.action.Kind = control.ActionHistoryPage
			case "old-detail":
				f.action.Kind = control.ActionHistoryDetail
			}
			f.app.HandleGatewayAction(t.Context(), f.action)
			if f.starts != 0 || len(f.commands) != 0 || f.app.surfaceResumeRuntime.groupOnDemandContinuations[f.action.SurfaceSessionID] != nil {
				t.Fatal("unsafe history action started runtime work")
			}
		})
	}
}

func TestTopicHistoryAfterIdleWorkerReleaseUsesPersistedBoundThread(t *testing.T) {
	f := newTopicHistoryRecoveryFixture(t)
	f.app.service.UpsertInstance(&state.InstanceRecord{InstanceID: "retired-worker", Online: true, Source: "headless", Managed: true,
		WorkspaceKey: f.workspace, WorkspaceRoot: f.workspace, Threads: map[string]*state.ThreadRecord{"thread-1": {ThreadID: "thread-1", CWD: f.workspace}}})
	surface := f.app.topicSurfaceLocked(f.action.SurfaceSessionID)
	surface.AttachedInstanceID, surface.SelectedThreadID, surface.RouteMode = "retired-worker", "thread-1", state.RouteModePinned
	surface.ClaimedWorkspaceKey = f.workspace
	f.app.mu.Lock()
	f.app.syncSurfaceResumeStateForSurfacesLocked([]string{surface.SurfaceSessionID}, nil)
	f.app.finishIdleHeadlessStopLocked(time.Now(), managedHeadlessIdleStopResult{Target: managedHeadlessIdleStopTarget{InstanceID: "retired-worker"}})
	f.app.mu.Unlock()
	if surface.AttachedInstanceID != "" || f.app.service.Instance("retired-worker") != nil {
		t.Fatal("idle worker was not released")
	}
	f.app.HandleGatewayAction(t.Context(), f.action)
	if f.starts != 1 || f.app.service.SurfaceSnapshot(surface.SurfaceSessionID).PendingHeadless.ThreadID != "thread-1" {
		t.Fatal("history did not restore the persisted conversation after idle release")
	}
}

func TestTopicColdHistoryDuplicateDoesNotOverwriteResumeContinuation(t *testing.T) {
	f := newTopicHistoryRecoveryFixture(t)
	f.app.HandleGatewayAction(t.Context(), f.action)
	original := f.app.surfaceResumeRuntime.groupOnDemandContinuations[f.action.SurfaceSessionID]
	duplicate := f.action
	duplicate.MessageID = "om_second"
	duplicate.Text = "/history"
	f.app.HandleGatewayAction(t.Context(), duplicate)
	if f.starts != 1 || f.app.surfaceResumeRuntime.groupOnDemandContinuations[f.action.SurfaceSessionID] != original || original.Action.MessageID != f.action.MessageID {
		t.Fatal("duplicate replaced original history continuation or started a second worker")
	}
}

func TestTopicHistoryRecoveryDoesNotOverwriteContinuationWhenTextIsBlocked(t *testing.T) {
	f := newTopicHistoryRecoveryFixture(t)
	f.app.HandleGatewayAction(t.Context(), f.action)
	original := f.app.surfaceResumeRuntime.groupOnDemandContinuations[f.action.SurfaceSessionID]
	pending := f.app.service.SurfaceSnapshot(f.action.SurfaceSessionID).PendingHeadless
	text := f.action
	text.Kind, text.MessageID, text.Text = control.ActionTextMessage, "om_text", "恢复后继续检查"
	f.app.HandleGatewayAction(t.Context(), text)
	if f.starts != 1 || f.app.surfaceResumeRuntime.groupOnDemandContinuations[f.action.SurfaceSessionID] != original {
		t.Fatal("additional text overwrote history recovery")
	}
	surface := f.app.topicSurfaceLocked(f.action.SurfaceSessionID)
	if surface.PendingTextInput != nil || len(surface.QueuedQueueItemIDs) != 0 {
		t.Fatal("pending text rejection behavior changed")
	}
	if len(f.gateway.captured) == 0 || !strings.Contains(operationCardText(f.gateway.captured[len(f.gateway.captured)-1]), "等待") {
		t.Fatal("pending text did not receive a waiting notice")
	}
	connectGroupOnDemandPendingForTest(f.app, pending.InstanceID, f.workspace)
	prompts, histories := 0, 0
	for _, command := range f.commands {
		if command.Kind == agentproto.CommandPromptSend {
			prompts++
			if command.Origin.MessageID != "om_text" {
				t.Fatal("history became a prompt")
			}
		}
		if command.Kind == agentproto.CommandThreadHistoryRead {
			histories++
		}
	}
	if prompts != 0 || histories != 1 {
		t.Fatalf("history replay after rejected text: prompts=%d histories=%d", prompts, histories)
	}
}

func TestTopicTextRecoveryAlsoChecksOwnerBeforeStarting(t *testing.T) {
	for _, owner := range []bool{false, true} {
		t.Run(fmt.Sprintf("owner=%t", owner), func(t *testing.T) {
			f := newTopicHistoryRecoveryFixture(t)
			f.action.Kind, f.action.Text = control.ActionTextMessage, "继续检查"
			if !owner {
				f.action.ActorUserID = "other"
			}
			f.app.HandleGatewayAction(t.Context(), f.action)
			want := 0
			if owner {
				want = 1
			}
			if f.starts != want {
				t.Fatalf("text recovery starts=%d, want %d", f.starts, want)
			}
		})
	}
}

func TestOrdinaryGroupColdHistoryRemainsUnavailableAndTextRecovers(t *testing.T) {
	for _, kind := range []control.ActionKind{control.ActionShowHistory, control.ActionTextMessage} {
		t.Run(string(kind), func(t *testing.T) {
			stateDir, workspace := t.TempDir(), t.TempDir()
			putGroupOnDemandResumeStateForTest(t, stateDir, workspace)
			app := newRestoreHintTestApp(stateDir)
			t.Cleanup(func() { _ = app.finishShutdown() })
			starts := 0
			app.startHeadless = func(relayruntime.HeadlessLaunchOptions) (int, error) { starts++; return 4321, nil }
			app.HandleGatewayAction(t.Context(), control.Action{Kind: kind, SurfaceSessionID: "feishu:app-1:chat:oc_room", GatewayID: "app-1", ChatID: "oc_room",
				ActorUserID: "ou_user", MessageID: "om_request", Text: "请求"})
			want := 0
			if kind == control.ActionTextMessage {
				want = 1
			}
			if starts != want {
				t.Fatalf("legacy group recovery changed: starts=%d want=%d", starts, want)
			}
		})
	}
}
