package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/frontstagecontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/core/threadcatalogcontract"
)

type fakeDesktopProjectCatalog struct {
	fakePersistedThreadCatalog
	snapshot threadcatalogcontract.DesktopProjectSnapshot
}

func (c *fakeDesktopProjectCatalog) DesktopProjects() (threadcatalogcontract.DesktopProjectSnapshot, error) {
	return c.snapshot, nil
}

func projectPickerFixture(t *testing.T) (*Service, *state.SurfaceConsoleRecord, *fakeDesktopProjectCatalog, map[string]string) {
	t.Helper()
	now := time.Now()
	svc := newServiceForTest(&now)
	svc.config.GitAvailable = false
	paths := map[string]string{}
	for _, id := range []string{"a", "b", "c"} {
		path := filepath.Join(t.TempDir(), id)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		paths[id] = state.ResolveWorkspaceClaimKey(path)
		svc.UpsertInstance(&state.InstanceRecord{InstanceID: "inst-" + id, WorkspaceRoot: path, Online: true,
			Threads: map[string]*state.ThreadRecord{id: {ThreadID: id, Name: "会话" + id, CWD: path, Loaded: true, LastUsedAt: now}},
		})
	}
	svc.root.Instances["inst-a"].Threads["unassigned"] = &state.ThreadRecord{ThreadID: "unassigned", Name: "未归属会话", CWD: paths["a"], Loaded: true, LastUsedAt: now}
	catalog := &fakeDesktopProjectCatalog{snapshot: threadcatalogcontract.DesktopProjectSnapshot{
		Available: true,
		Projects: []threadcatalogcontract.DesktopProject{
			{ID: "p1", Name: "臂环开发", RootPaths: []string{paths["a"], paths["b"]}},
			{ID: "p2", Name: "其他项目", RootPaths: []string{paths["c"]}},
		},
		ThreadProjectIDs: map[string]string{"unassigned": ""},
	}}
	svc.SetPersistedThreadCatalog(catalog)
	surface := svc.ensureSurface(control.Action{SurfaceSessionID: "surface", ChatID: "chat", ActorUserID: "user"})
	singleTargetPickerEvent(t, svc.openTargetPicker(surface, control.TargetPickerRequestSourceList, "", nil, "", false))
	return svc, surface, catalog, paths
}

func TestDesktopProjectPickerGroupsMultipleDirectoriesAndExplicitProjectless(t *testing.T) {
	svc, surface, catalog, _ := projectPickerFixture(t)
	record := svc.activeTargetPicker(surface)
	view := singleTargetPickerEvent(t, svc.handleTargetPickerSelectWorkspace(surface, record.PickerID, desktopProjectValuePrefix+"p1", "user", nil))
	if !view.ProjectGrouped || len(view.WorkspaceOptions) != 3 || view.WorkspaceOptions[0].Label != "臂环开发" {
		t.Fatalf("expected desktop projects and Chinese label: %#v", view)
	}
	for _, id := range []string{"a", "b"} {
		if _, ok := targetPickerSessionOption(view, targetPickerThreadValue(id)); !ok {
			t.Fatalf("project lost directory session %s: %#v", id, view.SessionOptions)
		}
	}
	if _, ok := targetPickerSessionOption(view, targetPickerThreadValue("unassigned")); ok {
		t.Fatal("explicit projectless thread was inferred into a project")
	}
	catalog.snapshot.Projects[0].Name = "臂环开发新名称"
	refreshed, err := svc.buildTargetPickerView(surface, record)
	if err != nil || refreshed.WorkspaceOptions[0].Label != "臂环开发新名称" {
		t.Fatalf("rename did not refresh: %#v, %v", refreshed.WorkspaceOptions, err)
	}
	view = singleTargetPickerEvent(t, svc.handleTargetPickerSelectWorkspace(surface, record.PickerID, desktopProjectValuePrefix+desktopProjectlessID, "user", nil))
	if _, ok := targetPickerSessionOption(view, targetPickerThreadValue("unassigned")); !ok {
		t.Fatalf("projectless thread was lost: %#v", view.SessionOptions)
	}
}

func TestDesktopProjectPickerRoutesToSelectedSessionRealWorkspace(t *testing.T) {
	svc, surface, _, paths := projectPickerFixture(t)
	record := svc.activeTargetPicker(surface)
	svc.handleTargetPickerSelectWorkspace(surface, record.PickerID, desktopProjectValuePrefix+"p1", "user", nil)
	view := singleTargetPickerEvent(t, svc.handleTargetPickerSelectSession(surface, record.PickerID, targetPickerThreadValue("b"), "user", nil))
	if view.SelectedWorkspaceKey != paths["b"] || view.WorkspaceSelectionValue != desktopProjectValuePrefix+"p1" {
		t.Fatalf("display group leaked into execution path: %#v", view)
	}
	events := svc.handleTargetPickerConfirm(surface, record.PickerID, "user", desktopProjectValuePrefix+"p1", targetPickerThreadValue("b"), nil)
	if surface.SelectedThreadID != "b" && (surface.PendingHeadless == nil || surface.PendingHeadless.ThreadID != "b") {
		t.Fatalf("confirm failed to route actual thread: %#v / %#v", surface, events)
	}
	if surface.PendingHeadless != nil && pendingHeadlessWorkspaceClaimKey(surface.PendingHeadless) != paths["b"] {
		t.Fatalf("wrong pending execution workspace: %#v", surface.PendingHeadless)
	}
}

func TestDesktopProjectPickerRevalidatesMembershipAndBusyWorkspaceAtConfirm(t *testing.T) {
	for _, change := range []string{"membership", "busy", "deleted"} {
		t.Run(change, func(t *testing.T) {
			svc, surface, catalog, paths := projectPickerFixture(t)
			record := svc.activeTargetPicker(surface)
			svc.handleTargetPickerSelectWorkspace(surface, record.PickerID, desktopProjectValuePrefix+"p1", "user", nil)
			svc.handleTargetPickerSelectSession(surface, record.PickerID, targetPickerThreadValue("b"), "user", nil)
			switch change {
			case "membership":
				catalog.snapshot.ThreadProjectIDs["b"] = "p2"
			case "busy":
				owner := svc.ensureSurface(control.Action{SurfaceSessionID: "owner", ChatID: "other-chat", ActorUserID: "other"})
				if !svc.claimWorkspace(owner, paths["b"]) {
					t.Fatal("claim failed")
				}
			case "deleted":
				catalog.snapshot.Projects = catalog.snapshot.Projects[1:]
			}
			events := svc.handleTargetPickerConfirm(surface, record.PickerID, "user", desktopProjectValuePrefix+"p1", targetPickerThreadValue("b"), nil)
			if surface.SelectedThreadID != "" || surface.PendingHeadless != nil {
				t.Fatalf("stale selection mutated routing: %#v", surface)
			}
			view := singleTargetPickerEvent(t, events)
			if view.SelectedSessionValue != "" || view.CanConfirm {
				t.Fatalf("stale target remains actionable: %#v", view)
			}
		})
	}
}

func TestDesktopProjectPickerSessionPaginationKeepsProjectScope(t *testing.T) {
	svc, surface, _, _ := projectPickerFixture(t)
	record := svc.activeTargetPicker(surface)
	svc.handleTargetPickerSelectWorkspace(surface, record.PickerID, desktopProjectValuePrefix+"p1", "user", nil)
	view := singleTargetPickerEvent(t, svc.handleTargetPickerPage(surface, record.PickerID, frontstagecontract.CardTargetPickerSessionFieldName, 1, "user", nil))
	if !view.ProjectGrouped || view.WorkspaceSelectionValue != desktopProjectValuePrefix+"p1" {
		t.Fatalf("pagination lost project scope: %#v", view)
	}
	if _, ok := targetPickerSessionOption(view, targetPickerThreadValue("b")); !ok {
		t.Fatal("pagination reverted to a single directory")
	}
}

func TestDesktopProjectPickerNewThreadRevalidatesChangedRoot(t *testing.T) {
	svc, surface, catalog, paths := projectPickerFixture(t)
	record := svc.activeTargetPicker(surface)
	view := singleTargetPickerEvent(t, svc.handleTargetPickerSelectWorkspace(surface, record.PickerID, desktopProjectValuePrefix+"p1", "user", nil))
	if view.SelectedSessionValue != targetPickerNewThreadValue || view.SelectedWorkspaceKey != paths["a"] {
		t.Fatalf("new session must use first project root: %#v", view)
	}
	catalog.snapshot.Projects[0].RootPaths = []string{paths["b"]}
	view = singleTargetPickerEvent(t, svc.handleTargetPickerConfirm(surface, record.PickerID, "user", desktopProjectValuePrefix+"p1", targetPickerNewThreadValue, nil))
	if surface.PendingHeadless != nil || surface.RouteMode == state.RouteModeNewThreadReady || len(view.Messages) == 0 {
		t.Fatalf("changed root must require fresh confirmation: %#v", view)
	}
}

func TestDesktopProjectPickerSharedRootDoesNotPromiseWrongProjectForNewThread(t *testing.T) {
	svc, surface, catalog, paths := projectPickerFixture(t)
	catalog.snapshot.Projects[1].RootPaths = []string{paths["a"]}
	catalog.snapshot.ThreadProjectIDs["a"] = "p2"
	record := svc.activeTargetPicker(surface)
	view := singleTargetPickerEvent(t, svc.handleTargetPickerSelectWorkspace(surface, record.PickerID, desktopProjectValuePrefix+"p2", "user", nil))
	if _, ok := targetPickerSessionOption(view, targetPickerThreadValue("a")); !ok {
		t.Fatal("explicit project assignment lost on shared directory")
	}
	if _, ok := targetPickerSessionOption(view, targetPickerNewThreadValue); ok {
		t.Fatal("new thread would infer into the other project sharing this root")
	}
}

func TestDesktopProjectPickerPublicCommandEntrypoints(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		kind   control.ActionKind
		source control.TargetPickerRequestSource
	}{
		{"list", control.ActionListInstances, control.TargetPickerRequestSourceList},
		{"use", control.ActionShowThreads, control.TargetPickerRequestSourceUse},
		{"useall", control.ActionShowAllThreads, control.TargetPickerRequestSourceUseAll},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			svc, surface, _, paths := projectPickerFixture(t)
			view := singleTargetPickerEvent(t, svc.ApplySurfaceAction(control.Action{
				Kind: testCase.kind, SurfaceSessionID: surface.SurfaceSessionID,
				ChatID: "chat", ActorUserID: "user", Text: "/" + testCase.name,
			}))
			if !view.ProjectGrouped || view.Source != testCase.source || len(view.WorkspaceOptions) != 3 {
				t.Fatalf("public command did not open project picker: source=%s grouped=%v", view.Source, view.ProjectGrouped)
			}
			record := svc.activeTargetPicker(surface)
			svc.handleTargetPickerSelectWorkspace(surface, record.PickerID, desktopProjectValuePrefix+"p1", "user", nil)
			view = singleTargetPickerEvent(t, svc.handleTargetPickerSelectSession(surface, record.PickerID, targetPickerThreadValue("b"), "user", nil))
			if view.SelectedWorkspaceKey != paths["b"] {
				t.Fatal("command entrypoint lost selected session directory")
			}
		})
	}
}
