package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestTargetPickerWorkspaceProjectionPreservesBackendOnlineAndFreshOwner(t *testing.T) {
	now := time.Now()
	svc := newServiceForTest(&now)
	svc.config.GitAvailable = false
	root := t.TempDir()
	live, historical := filepath.Join(root, "live"), filepath.Join(root, "historical")
	for _, dir := range []string{live, historical} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	svc.SetPersistedThreadCatalog(&fakePersistedThreadCatalog{
		recent:           []state.ThreadRecord{{ThreadID: "saved", CWD: historical, LastUsedAt: now}},
		recentWorkspaces: map[string]time.Time{historical: now},
	})
	for _, inst := range []*state.InstanceRecord{
		{InstanceID: "online-codex", WorkspaceRoot: live, Online: true, Backend: agentproto.BackendCodex},
		{InstanceID: "offline-codex", WorkspaceRoot: historical, Online: false, Backend: agentproto.BackendCodex},
		{InstanceID: "online-claude", WorkspaceRoot: historical, Online: true, Backend: agentproto.BackendClaude},
	} {
		svc.UpsertInstance(inst)
	}
	surface := svc.ensureSurface(control.Action{SurfaceSessionID: "picker", ChatID: "picker-chat", ActorUserID: "user"})
	entries := svc.targetPickerWorkspaceEntriesForMode(surface, targetPickerWorkspaceEntryModeList)
	if len(entries) != 2 {
		t.Fatalf("expected online and historical workspaces, got %#v", entries)
	}
	for _, entry := range entries {
		wantAttachable := entry.workspaceKey == state.ResolveWorkspaceClaimKey(live)
		if entry.attachable != wantAttachable || entry.recoverableOnly == wantAttachable {
			t.Fatalf("online/backend filtering changed: %#v", entry)
		}
	}
	owner := svc.ensureSurface(control.Action{SurfaceSessionID: "owner", ChatID: "other-chat", ActorUserID: "other-user"})
	if !svc.claimWorkspace(owner, live) {
		t.Fatal("could not claim live workspace")
	}
	entries = svc.targetPickerWorkspaceEntriesForMode(surface, targetPickerWorkspaceEntryModeList)
	if len(entries) != 1 || entries[0].workspaceKey != state.ResolveWorkspaceClaimKey(historical) {
		t.Fatalf("fresh owner must remove occupied non-Git workspace: %#v", entries)
	}
	svc.releaseSurfaceWorkspaceClaim(owner)
	if entries := svc.targetPickerWorkspaceEntriesForMode(surface, targetPickerWorkspaceEntryModeList); len(entries) != 2 {
		t.Fatalf("released workspace must return on next projection: %#v", entries)
	}
}

func BenchmarkTargetPickerManyHistoricalWorkspaces(b *testing.B) {
	for _, count := range []int{10, 100} {
		b.Run(fmt.Sprintf("workspaces_%d", count), func(b *testing.B) {
			root, now := b.TempDir(), time.Now()
			svc := newServiceForTest(&now)
			catalog := &fakePersistedThreadCatalog{recentWorkspaces: map[string]time.Time{}}
			for i := 0; i < count; i++ {
				dir := filepath.Join(root, fmt.Sprintf("workspace-%d", i))
				if err := os.Mkdir(dir, 0700); err != nil {
					b.Fatal(err)
				}
				catalog.recentWorkspaces[dir] = now
				catalog.recent = append(catalog.recent, state.ThreadRecord{ThreadID: fmt.Sprintf("saved-%d", i), CWD: dir, LastUsedAt: now})
			}
			svc.SetPersistedThreadCatalog(catalog)
			live := filepath.Join(root, "workspace-0")
			inst := &state.InstanceRecord{InstanceID: "online", WorkspaceRoot: live, Online: true, Threads: map[string]*state.ThreadRecord{}}
			for i := 0; i < 36; i++ {
				id := fmt.Sprintf("live-%d", i)
				inst.Threads[id] = &state.ThreadRecord{ThreadID: id, CWD: live, LastUsedAt: now}
			}
			svc.UpsertInstance(inst)
			surface := svc.ensureSurface(control.Action{SurfaceSessionID: "bench", ChatID: "chat", ActorUserID: "user"})
			record := &activeTargetPickerRecord{Source: control.TargetPickerRequestSourceList, AllowNewThread: true}
			view, err := svc.buildTargetPickerView(surface, record)
			if err != nil || len(view.WorkspaceOptions) != count {
				b.Fatalf("historical candidates lost: %d, %v", len(view.WorkspaceOptions), err)
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
