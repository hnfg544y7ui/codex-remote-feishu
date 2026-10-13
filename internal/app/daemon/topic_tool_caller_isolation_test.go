package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/orchestrator"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
)

type topicToolIsolationPermissionChecker struct{}

func (topicToolIsolationPermissionChecker) CheckPrimaryBotPermission(context.Context, orchestrator.PrimaryBotPermissionRequest) orchestrator.PrimaryBotPermissionDecision {
	return orchestrator.PrimaryBotPermissionDecision{Allowed: true, Scope: "im:message.group_msg"}
}

type topicToolIsolationFixture struct {
	app                *App
	workspace          string
	surfaceA, surfaceB string
}

func newTopicToolIsolationFixture(t *testing.T, activeA, activeB bool) topicToolIsolationFixture {
	t.Helper()
	workspace := state.NormalizeWorkspaceKey(t.TempDir())
	app := newRestoreHintTestApp(t.TempDir())
	app.service.SetPrimaryBotPermissionChecker(topicToolIsolationPermissionChecker{})
	app.service.ApplySurfaceAction(control.Action{Kind: control.ActionPrimaryCommand,
		SurfaceSessionID: "feishu:app-1:chat:oc_room", GatewayID: "app-1", ChatID: "oc_room", ActorUserID: "ou_owner", Text: "/primary on", Value: "on"})
	app.service.ApplySurfaceAction(control.Action{Kind: control.ActionCoworkersCommand,
		SurfaceSessionID: "feishu:app-1:chat:oc_room", GatewayID: "app-1", ChatID: "oc_room", ActorUserID: "ou_owner", Text: "/coworkers 2", Value: "2"})
	if limit := app.service.FeishuRoomConcurrencyLimit("feishu:app-1:chat:oc_room"); limit == nil || *limit != 2 {
		t.Fatal("fixture did not enable two coworkers")
	}
	surfaceA, _ := topicAppCreatedEvents(t, app, "om_tool_a")
	surfaceB, _ := topicAppCreatedEvents(t, app, "om_tool_b")
	for _, entry := range []struct{ instance, thread, surface, root, native string }{
		{"inst-topic-a", "thread-a", surfaceA, "om_tool_a", "omt_tool_a"},
		{"inst-topic-b", "thread-b", surfaceB, "om_tool_b", "omt_tool_b"},
	} {
		app.service.UpsertInstance(&state.InstanceRecord{InstanceID: entry.instance, WorkspaceRoot: workspace, WorkspaceKey: workspace,
			Source: "headless", Online: true, Threads: map[string]*state.ThreadRecord{entry.thread: {ThreadID: entry.thread, CWD: workspace, Loaded: true}}})
		surface := app.topicSurfaceLocked(entry.surface)
		surface.AttachedInstanceID = entry.instance
		surface.ClaimedWorkspaceKey = workspace
		if err := app.recordTopicDeliveryLocked(entry.surface, []feishu.Operation{{SurfaceSessionID: entry.surface, MessageID: entry.root + "_welcome", FeishuTopicID: entry.native, FeishuTopicRootMessageID: entry.root}}); err != nil {
			t.Fatal(err)
		}
		bindEvents := app.service.ApplySurfaceAction(control.Action{Kind: control.ActionUseThread, SurfaceSessionID: entry.surface,
			GatewayID: "app-1", ChatID: "oc_room", ActorUserID: "ou_owner", ThreadID: entry.thread})
		if surface.SelectedThreadID != entry.thread || surface.FeishuTopicBoundThreadID != entry.thread {
			for _, event := range bindEvents {
				if event.Notice != nil {
					t.Logf("bind fixture notice=%s text=%s", event.Notice.Code, event.Notice.Text)
				}
			}
			t.Fatalf("canonical fixture did not bind topic: selected=%s bound=%s", surface.SelectedThreadID, surface.FeishuTopicBoundThreadID)
		}

	}
	app.service.UpsertInstance(&state.InstanceRecord{InstanceID: "inst-idle", WorkspaceRoot: workspace, WorkspaceKey: workspace, Source: "headless", Online: true, Threads: map[string]*state.ThreadRecord{}})
	app.service.ApplySurfaceAction(control.Action{Kind: control.ActionStatus, SurfaceSessionID: "feishu:app-legacy:chat:oc_legacy", GatewayID: "app-legacy", ChatID: "oc_legacy", ActorUserID: "ou_legacy"})
	for _, surface := range app.service.Surfaces() {
		if surface.SurfaceSessionID == "feishu:app-legacy:chat:oc_legacy" {
			surface.AttachedInstanceID = "inst-idle"
			surface.ClaimedWorkspaceKey = workspace
		}
	}
	for _, entry := range []struct {
		surface, instance, thread, turn string
		active                          bool
	}{
		{surfaceA, "inst-topic-a", "thread-a", "turn-a", activeA},
		{surfaceB, "inst-topic-b", "thread-b", "turn-b", activeB},
	} {
		events := app.service.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: entry.surface,
			GatewayID: "app-1", ChatID: "oc_room", ActorUserID: "ou_owner", MessageID: entry.surface + "_input", Text: "read-only test"})
		foundPrompt := false
		for _, event := range events {
			if event.Command != nil && event.Command.Kind == agentproto.CommandPromptSend {
				foundPrompt = true
			}
		}
		if !foundPrompt {
			for _, event := range events {
				if event.Notice != nil {
					t.Logf("fixture blocked: %s %s", event.Notice.Code, event.Notice.Text)
				}
			}
			t.Fatalf("fixture topic %s did not dispatch an actual prompt.send", entry.surface)
		}

		if entry.active {
			app.service.ApplyAgentEvent(entry.instance, agentproto.Event{Kind: agentproto.EventTurnStarted,
				ThreadID: entry.thread, TurnID: entry.turn, Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: entry.surface}})
		}
	}
	wantActive := 0
	if activeA {
		wantActive++
	}
	if activeB {
		wantActive++
	}
	if got := app.service.ActiveRemoteTurns(); len(got) != wantActive {
		t.Fatalf("active setup=%#v expected %d", got, wantActive)
	}
	if got := app.service.PendingRemoteTurns(); len(got) != 2-wantActive {
		t.Fatalf("pending setup=%#v expected %d", got, 2-wantActive)
	}
	return topicToolIsolationFixture{app: app, workspace: workspace, surfaceA: surfaceA, surfaceB: surfaceB}
}

func TestTopicToolCallerSameWorkspaceIsolationAcrossActiveAndPending(t *testing.T) {
	for _, phase := range []struct {
		name             string
		activeA, activeB bool
	}{
		{"both active", true, true}, {"active and pending", true, false}, {"both pending", false, false},
	} {
		t.Run(phase.name, func(t *testing.T) {
			fixture := newTopicToolIsolationFixture(t, phase.activeA, phase.activeB)
			app := fixture.app
			for _, sidecarSurface := range []string{"feishu:app-legacy:chat:oc_legacy", fixture.surfaceB, fixture.surfaceA} {
				if err := writeWorkspaceSurfaceContext(fixture.workspace, workspaceSurfaceContextPayload{SurfaceSessionID: sidecarSurface,
					GatewayID: "app-legacy", ChatID: "oc_legacy", ActorUserID: "ou_legacy", WorkspaceKey: fixture.workspace}); err != nil {
					t.Fatal(err)
				}
				for _, target := range []struct{ caller, surface, root, native, thread string }{
					{"inst-topic-a", fixture.surfaceA, "om_tool_a", "omt_tool_a", "thread-a"},
					{"inst-topic-b", fixture.surfaceB, "om_tool_b", "omt_tool_b", "thread-b"},
				} {
					app.mu.Lock()
					resolved, err := app.resolveToolCallerSurfaceContextLocked(target.caller)
					app.mu.Unlock()
					if err != nil {
						t.Fatal(err)
					}
					if resolved.SurfaceSessionID != target.surface || resolved.AttachedInstanceID != target.caller || resolved.SelectedThreadID != target.thread || resolved.WorkspaceRoot != fixture.workspace {
						t.Fatalf("sidecar %s overwrote caller %s context: %#v", sidecarSurface, target.caller, resolved)
					}
					ref, ok := feishuidentity.ParseSurfaceRef(resolved.SurfaceSessionID)
					if !ok || ref.TopicRootMessageID() != target.root || app.topicSurfaceLocked(resolved.SurfaceSessionID).FeishuTopicID != target.native {
						t.Fatalf("native/root isolation lost %#v", resolved)
					}
				}
			}
			for _, caller := range []string{"inst-idle", "inst-unknown", ""} {
				app.mu.Lock()
				resolved, err := app.resolveToolCallerSurfaceContextLocked(caller)
				app.mu.Unlock()
				if err == nil || resolved.SurfaceSessionID != "" {
					t.Fatalf("nonremote caller %q acquired topic %#v err=%v", caller, resolved, err)
				}
			}
		})
	}
}

func TestTopicMCPMediaCallerSameWorkspaceFreezesDeliveryTarget(t *testing.T) {
	fixture := newTopicToolIsolationFixture(t, true, false)
	app := fixture.app
	if err := writeWorkspaceSurfaceContext(fixture.workspace, workspaceSurfaceContextPayload{SurfaceSessionID: fixture.surfaceB,
		GatewayID: "app-legacy", ChatID: "oc_legacy", WorkspaceKey: fixture.workspace}); err != nil {
		t.Fatal(err)
	}
	sender := &fakeToolSender{}
	check := func(surfaceID string) string {
		ref, ok := feishuidentity.ParseSurfaceRef(surfaceID)
		if !ok || !ref.IsTopic() {
			t.Fatalf("media target is not a topic: %s", surfaceID)
		}
		native := app.topicSurfaceLocked(surfaceID).FeishuTopicID
		expected := map[string]string{"om_tool_a": "omt_tool_a", "om_tool_b": "omt_tool_b"}
		if ref.TopicChatID() != "oc_room" || native != expected[ref.TopicRootMessageID()] {
			t.Fatalf("media native/root mismatch %s %s", ref.TopicRootMessageID(), native)
		}
		return ref.TopicRootMessageID()
	}
	sender.fileSendFn = func(_ context.Context, req feishu.IMFileSendRequest) (feishu.IMFileSendResult, error) {
		root := check(req.SurfaceSessionID)
		if req.ChatID != "oc_room" || req.GatewayID != "app-1" {
			t.Fatalf("file delivery crossrouted %#v", req)
		}
		return feishu.IMFileSendResult{SurfaceSessionID: req.SurfaceSessionID, FileName: "report.txt", MessageID: "om_file_" + root}, nil
	}
	sender.imageSendFn = func(_ context.Context, req feishu.IMImageSendRequest) (feishu.IMImageSendResult, error) {
		root := check(req.SurfaceSessionID)
		if req.ChatID != "oc_room" || req.GatewayID != "app-1" {
			t.Fatalf("image delivery crossrouted %#v", req)
		}
		return feishu.IMImageSendResult{SurfaceSessionID: req.SurfaceSessionID, ImageName: "preview.png", MessageID: "om_image_" + root}, nil
	}
	app.gateway = sender
	filePath := filepath.Join(fixture.workspace, "report.txt")
	imagePath := filepath.Join(fixture.workspace, "preview.png")
	for _, path := range []string{filePath, imagePath} {
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []struct{ caller, surface, spoof, root string }{
		{"inst-topic-a", fixture.surfaceA, fixture.surfaceB, "om_tool_a"},
		{"inst-topic-b", fixture.surfaceB, fixture.surfaceA, "om_tool_b"},
	} {
		ctx := withToolCallerInstanceID(t.Context(), target.caller)
		result, err := app.sendIMFileTool(ctx, map[string]any{"path": filePath, "surface_session_id": target.spoof})
		if err != nil || result["surface_session_id"] != target.surface {
			t.Fatalf("file caller isolation=%#v err=%v", result, err)
		}
		result, err = app.sendIMImageTool(ctx, map[string]any{"path": imagePath, "surface_session_id": target.spoof})
		if err != nil || result["surface_session_id"] != target.surface {
			t.Fatalf("image caller isolation=%#v err=%v", result, err)
		}
		for _, kind := range []string{"file", "image"} {
			messageID := fmt.Sprintf("om_%s_%s", kind, target.root)
			if got := app.lookupTopicMessageSurface("app-1", messageID, "oc_room"); got != target.surface {
				t.Fatalf("media receipt wrong owner %s %s", messageID, got)
			}
		}
	}
	beforeFiles, beforeImages := len(sender.fileCalls), len(sender.imageCalls)
	for _, caller := range []string{"inst-idle", "inst-unknown", ""} {
		ctx := withToolCallerInstanceID(t.Context(), caller)
		if _, err := app.sendIMFileTool(ctx, map[string]any{"path": filePath, "surface_session_id": fixture.surfaceA}); err == nil {
			t.Fatalf("idle/unknown file caller %q sent media", caller)
		}
		if _, err := app.sendIMImageTool(ctx, map[string]any{"path": imagePath, "surface_session_id": fixture.surfaceA}); err == nil {
			t.Fatalf("idle/unknown image caller %q sent media", caller)
		}
	}
	if len(sender.fileCalls) != beforeFiles || len(sender.imageCalls) != beforeImages {
		t.Fatal("rejected caller reached media delivery")
	}
}
