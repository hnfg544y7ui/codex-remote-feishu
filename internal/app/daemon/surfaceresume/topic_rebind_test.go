package surfaceresume

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func boundTopicRebindFixture(t *testing.T) (*Store, Entry) {
	t.Helper()
	store := NewStore(filepath.Join(t.TempDir(), StateFileName))
	entry := topicEntryForTest()
	entry.FeishuTopicID = "omt_native"
	entry.ResumeThreadID = "old-thread"
	if err := store.Put(entry); err != nil {
		t.Fatal(err)
	}
	entry, _ = store.Get(entry.SurfaceSessionID)
	return store, entry
}

func TestTopicRebindExplicitCASRoundTrip(t *testing.T) {
	store, previous := boundTopicRebindFixture(t)
	next := previous
	next.ResumeThreadID, next.FeishuTopicBoundThreadID = "new-thread", "new-thread"
	next.ResumeInstanceID, next.ResumeRouteMode = "next-instance", "pinned"
	if err := store.Put(next); err == nil {
		t.Fatal("ordinary Put accepted a rebind")
	}
	if err := store.ReplaceAll(map[string]Entry{next.SurfaceSessionID: next}); err == nil {
		t.Fatal("ordinary ReplaceAll accepted a rebind")
	}
	if err := store.RebindTopic("old-thread", next); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadStore(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := loaded.Get(next.SurfaceSessionID)
	if got.ResumeThreadID != "new-thread" || got.FeishuTopicBoundThreadID != "new-thread" || got.ResumeInstanceID != "next-instance" {
		t.Fatalf("new target did not survive restart: %#v", got)
	}
	if err := store.RebindTopic("old-thread", previous); err == nil {
		t.Fatal("stale CAS restored an old target")
	}
}

func TestTopicRebindRejectedMutationsKeepOldEntry(t *testing.T) {
	for name, mutate := range map[string]func(*Entry){
		"owner":                 func(e *Entry) { e.FeishuTopicOwnerID = "other" },
		"native":                func(e *Entry) { e.FeishuTopicID = "other" },
		"root":                  func(e *Entry) { e.FeishuTopicRootMessageID = "other" },
		"gateway":               func(e *Entry) { e.GatewayID = "other" },
		"chat":                  func(e *Entry) { e.ChatID = "other" },
		"closed":                func(e *Entry) { e.FeishuTopicClosed = true },
		"empty target":          func(e *Entry) { e.ResumeThreadID, e.FeishuTopicBoundThreadID = "", "" },
		"missing resume target": func(e *Entry) { e.ResumeThreadID = "" },
		"mismatched target":     func(e *Entry) { e.ResumeThreadID = "third-thread" },
		"different surface":     func(e *Entry) { e.SurfaceSessionID += "other" },
	} {
		t.Run(name, func(t *testing.T) {
			store, previous := boundTopicRebindFixture(t)
			before, err := os.ReadFile(store.Path())
			if err != nil {
				t.Fatal(err)
			}
			next := previous
			next.ResumeThreadID, next.FeishuTopicBoundThreadID = "new-thread", "new-thread"
			mutate(&next)
			if err := store.RebindTopic("old-thread", next); err == nil {
				t.Fatal("invalid rebind accepted")
			}
			got, _ := store.Get(previous.SurfaceSessionID)
			if !SameEntryContent(got, previous) {
				t.Fatal("rejected rebind changed memory")
			}
			after, err := os.ReadFile(store.Path())
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejected rebind changed disk")
			}
		})
	}
}

func TestTopicRebindClosedOrUnboundCannotBeReopened(t *testing.T) {
	for _, closed := range []bool{false, true} {
		store, previous := boundTopicRebindFixture(t)
		if closed {
			previous.FeishuTopicClosed = true
			if err := store.Put(previous); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := store.Delete(previous.SurfaceSessionID); err != nil {
				t.Fatal(err)
			}
			previous.ResumeThreadID, previous.FeishuTopicBoundThreadID = "", ""
			if err := store.Put(previous); err != nil {
				t.Fatal(err)
			}
		}
		next := previous
		next.ResumeThreadID, next.FeishuTopicBoundThreadID, next.FeishuTopicClosed = "new-thread", "new-thread", false
		if err := store.RebindTopic("old-thread", next); err == nil {
			t.Fatal("closed or unbound topic rebound")
		}
		got, _ := store.Get(previous.SurfaceSessionID)
		if !SameEntryContent(got, previous) {
			t.Fatal("rejected rebind changed old entry")
		}
	}
}

func TestTopicRebindWriteFailureRetainsMemoryAndDestination(t *testing.T) {
	store, previous := boundTopicRebindFixture(t)
	if err := os.Remove(store.Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(store.Path(), 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(store.Path(), "keep")
	if err := os.WriteFile(marker, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	next := previous
	next.ResumeThreadID, next.FeishuTopicBoundThreadID = "new-thread", "new-thread"
	if err := store.RebindTopic("old-thread", next); err == nil {
		t.Fatal("expected atomic write failure")
	}
	got, _ := store.Get(previous.SurfaceSessionID)
	if !SameEntryContent(got, previous) {
		t.Fatal("failed persistence changed memory")
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "unchanged" {
		t.Fatal("failed persistence changed destination")
	}
}

func TestTopicRebindCannotChangeBackendOrProfileContract(t *testing.T) {
	for name, mutate := range map[string]func(*Entry){
		"backend":          func(e *Entry) { e.Backend, e.ClaudeProfileID = "claude", "claude-other" },
		"mode":             func(e *Entry) { e.ProductMode = "vscode" },
		"codex profile":    func(e *Entry) { e.CodexProfileID = "cp_other" },
		"claude profile":   func(e *Entry) { e.Backend, e.ClaudeProfileID = "claude", "claude-other" },
		"opencode profile": func(e *Entry) { e.Backend, e.OpenCodeProfileID = "opencode", "oc_other" },
	} {
		t.Run(name, func(t *testing.T) {
			store, previous := boundTopicRebindFixture(t)
			if name == "claude profile" || name == "opencode profile" {
				if name == "claude profile" {
					previous.Backend, previous.ClaudeProfileID = "claude", "claude-original"
				} else {
					previous.Backend, previous.OpenCodeProfileID = "opencode", "oc_original"
				}
				if err := store.Put(previous); err != nil {
					t.Fatal(err)
				}
				previous, _ = store.Get(previous.SurfaceSessionID)
			}
			next := previous
			next.ResumeThreadID, next.FeishuTopicBoundThreadID = "new-thread", "new-thread"
			mutate(&next)
			if err := store.RebindTopic("old-thread", next); err == nil {
				t.Fatal("backend or profile change accepted")
			}
			got, _ := store.Get(previous.SurfaceSessionID)
			if !SameEntryContent(got, previous) {
				t.Fatal("rejected backend change mutated store")
			}
		})
	}
}
