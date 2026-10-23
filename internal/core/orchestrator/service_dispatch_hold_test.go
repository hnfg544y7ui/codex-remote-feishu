package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func holdTestBind(t *testing.T, svc *Service, instanceID string) {
	t.Helper()
	svc.UpsertInstance(&state.InstanceRecord{
		InstanceID: instanceID, WorkspaceRoot: "/data/repo", WorkspaceKey: "/data/repo", Online: true,
		ObservedFocusedThreadID: "thread-1",
		Threads:                 map[string]*state.ThreadRecord{"thread-1": {ThreadID: "thread-1", CWD: "/data/repo", Loaded: true}},
	})
	for _, action := range []control.Action{
		{Kind: control.ActionAttachInstance, InstanceID: instanceID},
		{Kind: control.ActionUseThread, ThreadID: "thread-1"},
	} {
		action.SurfaceSessionID, action.ChatID, action.ActorUserID = "surface-1", "chat-1", "user-1"
		events := svc.ApplySurfaceAction(action)
		if svc.surfaceDispatchHeld(svc.root.Surfaces["surface-1"]) {
			assertHoldNoPrompt(t, events)
		}
	}
	if surface := svc.root.Surfaces["surface-1"]; surface.AttachedInstanceID != instanceID || surface.SelectedThreadID != "thread-1" {
		t.Fatalf("binding failed: %+v", surface)
	}
}

func assertHoldNoPrompt(t *testing.T, events []eventcontract.Event) {
	t.Helper()
	for _, event := range events {
		if event.Command != nil && event.Command.Kind == agentproto.CommandPromptSend {
			t.Fatalf("maintenance hold dispatched prompt: %+v", event.Command)
		}
	}
}

func TestMaintenanceDispatchHoldSurvivesDisconnectRebindAndResume(t *testing.T) {
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	holdTestBind(t, svc, "inst-before")
	for _, messageID := range []string{"active", "queued"} {
		svc.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: "surface-1",
			ChatID: "chat-1", ActorUserID: "user-1", MessageID: messageID, Text: messageID})
	}
	surface := svc.root.Surfaces["surface-1"]
	if surface.ActiveQueueItemID == "" || len(surface.QueuedQueueItemIDs) != 1 {
		t.Fatalf("expected active plus queued fixture: %+v", surface)
	}
	activeID, queuedID := surface.ActiveQueueItemID, surface.QueuedQueueItemIDs[0]
	queuedItem := surface.QueueItems[queuedID]
	image := &state.StagedImageRecord{ImageID: "queued-image", SourceMessageID: "queued", LocalPath: "/saved/image.png", State: state.ImageBound}
	file := &state.StagedFileRecord{FileID: "queued-file", SourceMessageID: "queued", LocalPath: "/saved/input.txt", State: state.FileBound}
	surface.StagedImages[image.ImageID], surface.StagedFiles[file.FileID] = image, file
	queuedItem.Inputs = append(queuedItem.Inputs, agentproto.Input{Type: "image", Path: image.LocalPath}, agentproto.Input{Type: "file", Path: file.LocalPath})
	surface.AutoContinue.Enabled = true
	surface.AutoContinue.Episode = &state.PendingAutoContinueEpisodeRecord{EpisodeID: "old-continuation", State: state.AutoContinueEpisodeScheduled}
	svc.HoldSurfaceDispatch(" surface-1 ")
	if surface.ActiveQueueItemID != activeID || surface.DispatchMode != state.DispatchModeNormal {
		t.Fatal("hold stopped active work or changed ordinary dispatch mode")
	}
	assertHoldNoPrompt(t, svc.ApplyInstanceDisconnected("inst-before"))
	if surface.ActiveQueueItemID != "" || surface.QueueItems[activeID].Status != state.QueueItemFailed {
		t.Fatal("disconnect did not terminate only the previously active item")
	}
	if !svc.surfaceDispatchHeld(surface) || surface.DispatchMode != state.DispatchModeNormal {
		t.Fatal("disconnect removed maintenance barrier")
	}
	if surface.AutoContinue.Episode != nil {
		t.Fatal("disconnect retained old automatic continuation")
	}
	holdTestBind(t, svc, "inst-after")
	assertHoldNoPrompt(t, svc.ResumeSurfaceDispatch("surface-1", nil))
	assertHoldNoPrompt(t, svc.dispatchNextWithOptions(surface, dispatchNextOptions{}))
	now = now.Add(time.Hour)
	assertHoldNoPrompt(t, svc.Tick(now))
	if surface.ActiveQueueItemID != "" || len(surface.QueuedQueueItemIDs) != 1 ||
		surface.QueuedQueueItemIDs[0] != queuedID || surface.QueueItems[queuedID] != queuedItem || queuedItem.Status != state.QueueItemQueued {
		t.Fatalf("maintenance barrier consumed or changed unsent queue: active=%q queue=%v item=%+v", surface.ActiveQueueItemID, surface.QueuedQueueItemIDs, queuedItem)
	}
	if surface.StagedImages[image.ImageID] != image || surface.StagedFiles[file.FileID] != file ||
		image.State != state.ImageBound || file.State != state.FileBound || len(queuedItem.Inputs) != 3 ||
		queuedItem.Inputs[1].Path != image.LocalPath || queuedItem.Inputs[2].Path != file.LocalPath {
		t.Fatal("maintenance recovery discarded or changed queued attachments")
	}
	svc.ReleaseSurfaceDispatchHold(" surface-1 ")
	if surface.ActiveQueueItemID != "" || len(surface.QueuedQueueItemIDs) != 1 {
		t.Fatal("releasing barrier dispatched or removed queue")
	}
	events := svc.ResumeSurfaceDispatch("surface-1", nil)
	if findAgentCommand(events, agentproto.CommandPromptSend) == nil || surface.ActiveQueueItemID != queuedID || len(surface.QueuedQueueItemIDs) != 0 {
		t.Fatalf("explicit resume did not dispatch original queued item: %+v", events)
	}
	if surface.QueueItems[activeID].Status != state.QueueItemFailed {
		t.Fatal("interrupted work was replayed")
	}
}

func TestMaintenanceDispatchHoldDoesNotReplaceOrdinaryPause(t *testing.T) {
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	holdTestBind(t, svc, "inst-1")
	svc.PauseSurfaceDispatch("surface-1")
	surface := svc.root.Surfaces["surface-1"]
	svc.HoldSurfaceDispatch("surface-1")
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: "surface-1",
		ChatID: "chat-1", ActorUserID: "user-1", MessageID: "queued", Text: "queued"})
	svc.ReleaseSurfaceDispatchHold("surface-1")
	assertHoldNoPrompt(t, svc.dispatchNext(surface))
	if surface.DispatchMode != state.DispatchModePausedForLocal || len(surface.QueuedQueueItemIDs) != 1 {
		t.Fatal("barrier release cleared ordinary pause or queue")
	}
	if findAgentCommand(svc.ResumeSurfaceDispatch("surface-1", nil), agentproto.CommandPromptSend) == nil {
		t.Fatal("ordinary explicit resume stopped working")
	}
}

func TestMaintenanceDispatchHoldBlocksDirectAutoContinue(t *testing.T) {
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	holdTestBind(t, svc, "inst-1")
	surface := svc.root.Surfaces["surface-1"]
	surface.AutoContinue.Enabled = true
	episode := &state.PendingAutoContinueEpisodeRecord{EpisodeID: "autocontinue-held", State: state.AutoContinueEpisodeScheduled}
	surface.AutoContinue.Episode = episode
	svc.HoldSurfaceDispatch("surface-1")
	if svc.autoContinueDispatchReady(surface) {
		t.Fatal("auto continue reported dispatch-ready while held")
	}
	assertHoldNoPrompt(t, svc.maybeDispatchPendingAutoContinue(surface, now))
	assertHoldNoPrompt(t, svc.dispatchAutoContinueEpisode(surface, svc.root.Instances["inst-1"], episode))
	if surface.AutoContinue.Episode != episode || len(surface.QueueItems) != 0 || surface.ActiveQueueItemID != "" {
		t.Fatal("held auto continuation mutated runtime or enqueued work")
	}
}
