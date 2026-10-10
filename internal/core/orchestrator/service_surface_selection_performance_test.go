package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestInstanceWorkspaceSelectionKeysKeepsVisibleDistinctPaths(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	for _, dir := range []string{first, second} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	inst := &state.InstanceRecord{Threads: map[string]*state.ThreadRecord{}}
	for i := 0; i < 44; i++ {
		id := fmt.Sprintf("first-%d", i)
		inst.Threads[id] = &state.ThreadRecord{ThreadID: id, WorkspaceKey: first, CWD: filepath.Join(first, "child")}
	}
	inst.Threads["cwd-fallback"] = &state.ThreadRecord{ThreadID: "cwd-fallback", CWD: second}
	inst.Threads["archived"] = &state.ThreadRecord{ThreadID: "archived", CWD: filepath.Join(root, "archived"), Archived: true}
	inst.Threads["helper"] = &state.ThreadRecord{ThreadID: "helper", CWD: filepath.Join(root, "helper"), TrafficClass: agentproto.TrafficClassInternalHelper}
	inst.Threads["nil"] = nil
	want := []string{state.ResolveWorkspaceClaimKey(first), state.ResolveWorkspaceClaimKey(second)}
	sort.Strings(want)
	if got := instanceWorkspaceSelectionKeys(inst); !reflect.DeepEqual(got, want) {
		t.Fatalf("workspace keys = %v, want %v", got, want)
	}
}

func TestInstanceWorkspaceSelectionKeysResolvesAliasesPerCall(t *testing.T) {
	root := t.TempDir()
	first, second, alias := filepath.Join(root, "first"), filepath.Join(root, "second"), filepath.Join(root, "alias")
	for _, dir := range []string{first, second} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(first, alias); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	inst := &state.InstanceRecord{Threads: map[string]*state.ThreadRecord{
		"real":       {ThreadID: "real", CWD: first},
		"alias":      {ThreadID: "alias", CWD: alias},
		"alias-copy": {ThreadID: "alias-copy", CWD: alias},
	}}
	want := []string{state.ResolveWorkspaceClaimKey(first)}
	if got := instanceWorkspaceSelectionKeys(inst); !reflect.DeepEqual(got, want) {
		t.Fatalf("alias keys = %v, want %v", got, want)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, alias); err != nil {
		t.Fatal(err)
	}
	want = []string{state.ResolveWorkspaceClaimKey(first), state.ResolveWorkspaceClaimKey(second)}
	sort.Strings(want)
	if got := instanceWorkspaceSelectionKeys(inst); !reflect.DeepEqual(got, want) {
		t.Fatalf("keys after retargeting symlink = %v, want %v", got, want)
	}
}

func TestInstanceWorkspaceSelectionKeysEmptyVisibleSetUsesRoot(t *testing.T) {
	root := t.TempDir()
	inst := &state.InstanceRecord{WorkspaceRoot: root, WorkspaceKey: root, Threads: map[string]*state.ThreadRecord{
		"archived": {ThreadID: "archived", CWD: filepath.Join(root, "other"), Archived: true},
	}}
	want := []string{state.ResolveWorkspaceClaimKey(root)}
	if got := instanceWorkspaceSelectionKeys(inst); !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback keys = %v, want %v", got, want)
	}
}

func TestInstanceWorkspaceSelectionKeysDoesNotMergeDifferentCWDs(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	for _, dir := range []string{first, second} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	inst := &state.InstanceRecord{WorkspaceRoot: root, Threads: map[string]*state.ThreadRecord{
		"first":  {ThreadID: "first", WorkspaceKey: first, CWD: first},
		"second": {ThreadID: "second", WorkspaceKey: first, CWD: second},
	}}
	want := []string{state.ResolveWorkspaceClaimKey(first), state.ResolveWorkspaceClaimKey(second)}
	sort.Strings(want)
	if got := instanceWorkspaceSelectionKeys(inst); !reflect.DeepEqual(got, want) {
		t.Fatalf("stale shared WorkspaceKey must retain different CWDs: got %v, want %v", got, want)
	}
}

func TestWorkspaceMembershipPreservesRelativePathResolutionBoundary(t *testing.T) {
	inst := &state.InstanceRecord{WorkspaceRoot: "relative-workspace", Source: "headless", Managed: true}
	if cwdBelongsToInstanceWorkspace(inst, "./relative-workspace") {
		t.Fatal("a host-resolved relative CWD must not match an unresolved workspace token")
	}
}

func TestMergedThreadProjectionRefreshesWorkspaceAliases(t *testing.T) {
	root := t.TempDir()
	first, second, alias := filepath.Join(root, "first"), filepath.Join(root, "second"), filepath.Join(root, "alias")
	for _, dir := range []string{first, second} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(first, alias); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	now := time.Now()
	svc := newServiceForTest(&now)
	svc.UpsertInstance(&state.InstanceRecord{InstanceID: "alias-projection", WorkspaceRoot: root, Threads: map[string]*state.ThreadRecord{
		"alias": {ThreadID: "alias", CWD: alias},
	}})
	assertWorkspace := func(want string) {
		t.Helper()
		views := svc.mergedThreadViews(nil)
		if len(views) != 1 || threadWorkspaceKey(views[0]) != state.ResolveWorkspaceClaimKey(want) {
			t.Fatalf("fresh projection did not resolve %s", want)
		}
	}
	assertWorkspace(first)
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, alias); err != nil {
		t.Fatal(err)
	}
	assertWorkspace(second)
}

func BenchmarkInstanceWorkspaceSelectionKeysRepeatedDirectory(b *testing.B) {
	for _, count := range []int{1, 44, 440} {
		b.Run(fmt.Sprintf("threads_%d", count), func(b *testing.B) {
			root := b.TempDir()
			inst := &state.InstanceRecord{WorkspaceRoot: root, WorkspaceKey: root, Threads: map[string]*state.ThreadRecord{}}
			for i := 0; i < count; i++ {
				id := fmt.Sprintf("thread-%d", i)
				inst.Threads[id] = &state.ThreadRecord{ThreadID: id, CWD: root}
			}
			if got := instanceWorkspaceSelectionKeys(inst); len(got) != 1 {
				b.Fatalf("expected one workspace, got %v", got)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				instanceWorkspaceSelectionKeys(inst)
			}
		})
	}
}

func BenchmarkTargetPickerRepeatedDirectory(b *testing.B) {
	for _, count := range []int{1, 44, 200} {
		b.Run(fmt.Sprintf("threads_%d", count), func(b *testing.B) {
			root := b.TempDir()
			now := time.Now()
			svc := newServiceForTest(&now)
			inst := &state.InstanceRecord{InstanceID: "picker-bench", WorkspaceRoot: root, WorkspaceKey: root, Online: true, Threads: map[string]*state.ThreadRecord{}}
			for i := 0; i < count; i++ {
				id := fmt.Sprintf("thread-%d", i)
				inst.Threads[id] = &state.ThreadRecord{ThreadID: id, CWD: root, LastUsedAt: now}
			}
			svc.UpsertInstance(inst)
			surface := svc.ensureSurface(control.Action{SurfaceSessionID: "picker-bench", ChatID: "chat", ActorUserID: "user"})
			record := &activeTargetPickerRecord{Source: control.TargetPickerRequestSourceUseAll, AllowNewThread: true}
			view, err := svc.buildTargetPickerView(surface, record)
			if err != nil || len(view.SessionOptions) != count+1 {
				b.Fatalf("expected all %d sessions and new-session option, got %d: %v", count, len(view.SessionOptions), err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := svc.buildTargetPickerView(surface, record); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
