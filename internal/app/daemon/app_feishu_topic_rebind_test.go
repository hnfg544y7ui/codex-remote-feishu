package daemon

import (
	"os"
	"reflect"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/app/daemon/surfaceresume"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func topicRebindCandidate(t *testing.T) (*App, *state.SurfaceConsoleRecord, *state.SurfaceConsoleRecord) {
	t.Helper()
	f := newTopicHistoryRecoveryFixture(t)
	app := f.app
	previous := app.topicSurfaceLocked(f.action.SurfaceSessionID)
	app.service.UpsertInstance(&state.InstanceRecord{InstanceID: "old-instance", WorkspaceRoot: f.workspace, WorkspaceKey: f.workspace,
		Online: true, Managed: true, Source: "headless", Threads: map[string]*state.ThreadRecord{
			"thread-1": {ThreadID: "thread-1", Name: "旧的会话", CWD: f.workspace, WorkspaceKey: f.workspace, Loaded: true},
		}})
	previous.AttachedInstanceID, previous.SelectedThreadID = "old-instance", "thread-1"
	app.service.UpsertInstance(&state.InstanceRecord{InstanceID: "new-instance", WorkspaceRoot: f.workspace, WorkspaceKey: f.workspace,
		Online: true, Managed: false, Source: "vscode", Threads: map[string]*state.ThreadRecord{
			"new-thread": {ThreadID: "new-thread", Name: "新的会话", CWD: f.workspace, WorkspaceKey: f.workspace, Loaded: true},
		}})
	next := *previous
	next.AttachedInstanceID, next.SelectedThreadID, next.FeishuTopicBoundThreadID = "new-instance", "new-thread", "new-thread"
	next.RouteMode, next.PendingHeadless, next.ClaimedWorkspaceKey = state.RouteModePinned, nil, f.workspace
	return app, previous, &next
}

func TestCommitTopicThreadRebindPersistsBeforeLiveMutation(t *testing.T) {
	app, previous, next := topicRebindCandidate(t)
	before := *previous
	if err := app.CommitTopicThreadRebind("thread-1", next); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, *previous) {
		t.Fatal("committer changed live surface")
	}
	loaded, err := surfaceresume.LoadStore(app.surfaceResumeRuntime.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.Get(next.SurfaceSessionID)
	if !ok || got.FeishuTopicBoundThreadID != "new-thread" || got.ResumeThreadID != "new-thread" || got.ResumeInstanceID != "new-instance" ||
		got.ResumeThreadTitle != "新的会话" || got.ResumeHeadless || got.ResumeWorkspaceKey != state.ResolveWorkspaceClaimKey(next.ClaimedWorkspaceKey) {
		t.Fatalf("candidate target metadata was not persisted: %#v", got)
	}
	if route := app.resolveTopicSurface(next.GatewayID, next.ChatID, next.FeishuTopicID, next.FeishuTopicRootMessageID); route != next.SurfaceSessionID {
		t.Fatal("topic routing identity changed")
	}
}

func TestCommitTopicThreadRebindRejectsInvalidCandidates(t *testing.T) {
	for name, mutate := range map[string]func(*App, *state.SurfaceConsoleRecord){
		"owner":            func(_ *App, s *state.SurfaceConsoleRecord) { s.FeishuTopicOwnerID = "other" },
		"native":           func(_ *App, s *state.SurfaceConsoleRecord) { s.FeishuTopicID = "other" },
		"root":             func(_ *App, s *state.SurfaceConsoleRecord) { s.FeishuTopicRootMessageID = "other" },
		"gateway":          func(_ *App, s *state.SurfaceConsoleRecord) { s.GatewayID = "other" },
		"chat":             func(_ *App, s *state.SurfaceConsoleRecord) { s.ChatID = "other" },
		"closed":           func(_ *App, s *state.SurfaceConsoleRecord) { s.FeishuTopicClosed = true },
		"empty target":     func(_ *App, s *state.SurfaceConsoleRecord) { s.SelectedThreadID = "" },
		"missing instance": func(_ *App, s *state.SurfaceConsoleRecord) { s.AttachedInstanceID = "missing" },
		"wrong route":      func(_ *App, s *state.SurfaceConsoleRecord) { s.RouteMode = state.RouteModeUnbound },
		"degraded store": func(a *App, _ *state.SurfaceConsoleRecord) {
			a.surfaceResumeRuntime.status = persistedStoreStatusDegraded
		},
	} {
		t.Run(name, func(t *testing.T) {
			app, previous, next := topicRebindCandidate(t)
			before := *previous
			persisted, _ := app.surfaceResumeRuntime.store.Get(previous.SurfaceSessionID)
			mutate(app, next)
			if err := app.CommitTopicThreadRebind("thread-1", next); err == nil {
				t.Fatal("invalid rebind accepted")
			}
			if !reflect.DeepEqual(before, *previous) {
				t.Fatal("rejected rebind changed live surface")
			}
			got, _ := app.surfaceResumeRuntime.store.Get(previous.SurfaceSessionID)
			if !surfaceresume.SameEntryContent(persisted, got) {
				t.Fatal("rejected rebind changed persisted target")
			}
		})
	}
}

func TestCommitTopicThreadRebindFailureRetainsLiveAndDurableTargets(t *testing.T) {
	app, previous, next := topicRebindCandidate(t)
	before := *previous
	if err := app.CommitTopicThreadRebind("wrong-old-thread", next); err == nil {
		t.Fatal("stale binding accepted")
	}
	path := app.surfaceResumeRuntime.store.Path()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := app.CommitTopicThreadRebind("thread-1", next); err == nil {
		t.Fatal("write failure accepted")
	}
	if !reflect.DeepEqual(before, *previous) {
		t.Fatal("failed commit changed live surface")
	}
	got, _ := app.surfaceResumeRuntime.store.Get(previous.SurfaceSessionID)
	if got.ResumeThreadID != "thread-1" || got.FeishuTopicBoundThreadID != "thread-1" {
		t.Fatal("failed commit changed durable target")
	}
}

func TestPendingTopicSwitchKeepsCommittedResumeUntilCAS(t *testing.T) {
	for _, attached := range []bool{true, false} {
		t.Run(map[bool]string{true: "attached", false: "old worker reclaimed"}[attached], func(t *testing.T) {
			app, previous, next := topicRebindCandidate(t)
			if !attached {
				previous.AttachedInstanceID, previous.SelectedThreadID, previous.ClaimedWorkspaceKey = "", "", ""
			}
			previous.PendingHeadless = &state.HeadlessLaunchRecord{ThreadID: "new-thread", WorkspaceKey: next.ClaimedWorkspaceKey, ThreadCWD: next.ClaimedWorkspaceKey}
			oldEntry, _ := app.surfaceResumeRuntime.store.Get(previous.SurfaceSessionID)
			projected, ok := app.currentSurfaceResumeEntryLocked(previous, false)
			if !ok || projected.ResumeThreadID != "thread-1" || projected.FeishuTopicBoundThreadID != "thread-1" ||
				projected.ResumeThreadCWD != oldEntry.ResumeThreadCWD || projected.ResumeInstanceID != oldEntry.ResumeInstanceID ||
				projected.ResumeHeadless != oldEntry.ResumeHeadless {
				t.Fatalf("pending switch replaced committed target: %#v", projected)
			}
			if err := app.persistTopicSurfaceLocked(previous); err != nil {
				t.Fatal(err)
			}
			loaded, err := surfaceresume.LoadStore(app.surfaceResumeRuntime.store.Path())
			if err != nil {
				t.Fatal(err)
			}
			got, _ := loaded.Get(previous.SurfaceSessionID)
			if got.ResumeThreadID != "thread-1" || got.FeishuTopicBoundThreadID != "thread-1" {
				t.Fatal("pending target leaked to disk")
			}
			if err := app.CommitTopicThreadRebind("thread-1", next); err != nil {
				t.Fatal(err)
			}
			loaded, err = surfaceresume.LoadStore(app.surfaceResumeRuntime.store.Path())
			if err != nil {
				t.Fatal(err)
			}
			got, _ = loaded.Get(previous.SurfaceSessionID)
			if got.ResumeThreadID != "new-thread" || got.FeishuTopicBoundThreadID != "new-thread" {
				t.Fatal("CAS did not publish new target")
			}
		})
	}
}
