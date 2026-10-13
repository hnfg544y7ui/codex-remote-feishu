package surfaceresume

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
)

func topicEntryForTest() Entry {
	return Entry{SurfaceSessionID: feishuidentity.TopicRef("bot", "oc_room", "om_root").SurfaceID(),
		ActorUserID: "ou_owner", FeishuTopicOwnerID: "ou_owner", ProductMode: "normal", FeishuTopicTitle: "测试话题"}
}

func TestTopicResumePendingAndUnboundRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFileName)
	store := NewStore(path)
	entry := topicEntryForTest()
	entry.CodexProfileID = "custom-profile"
	if err := store.Put(entry); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.Get(entry.SurfaceSessionID)
	if !ok || got.FeishuTopicID != "" || got.ResumeThreadID != "" || got.FeishuTopicBoundThreadID != "" ||
		got.FeishuTopicRootMessageID != "om_root" || got.ChatID != "oc_room" || got.GatewayID != "bot" ||
		got.FeishuTopicOwnerID != "ou_owner" || got.CodexProfileID != "custom-profile" {
		t.Fatalf("pending unbound topic failed round trip: %#v", got)
	}
	got.FeishuTopicID = "omt_native"
	got.ResumeThreadID = "codex-thread"
	got.FeishuTopicMessageURL = "https://example.invalid/message"
	got.FeishuTopicMessageIDs = append(got.FeishuTopicMessageIDs, "om_card")
	got.FeishuTopicClosed = true
	if err := loaded.Put(got); err != nil {
		t.Fatal(err)
	}
	restarted, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = restarted.Get(entry.SurfaceSessionID)
	if got.FeishuTopicBoundThreadID != "codex-thread" || !got.FeishuTopicClosed || got.FeishuTopicID != "omt_native" ||
		len(got.FeishuTopicMessageIDs) != 2 || got.FeishuTopicMessageURL == "" {
		t.Fatalf("bound closed topic failed restart: %#v", got)
	}
}

func TestTopicResumeRejectsIdentityAndTargetConflicts(t *testing.T) {
	for name, mutate := range map[string]func(*Entry){
		"gateway":            func(e *Entry) { e.GatewayID = "other" },
		"chat":               func(e *Entry) { e.ChatID = "other" },
		"root":               func(e *Entry) { e.FeishuTopicRootMessageID = "other" },
		"owner_missing":      func(e *Entry) { e.FeishuTopicOwnerID = "" },
		"binding":            func(e *Entry) { e.FeishuTopicBoundThreadID = "a"; e.ResumeThreadID = "b" },
		"metadata_on_legacy": func(e *Entry) { e.SurfaceSessionID = "feishu:bot:chat:oc_room"; e.FeishuTopicID = "omt_id" },
	} {
		t.Run(name, func(t *testing.T) {
			entry := topicEntryForTest()
			mutate(&entry)
			if _, ok := NormalizeEntry(entry); ok {
				t.Fatal("conflicting entry accepted")
			}
		})
	}
}

func TestTopicResumeBindingImmutableButActorMayChange(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), StateFileName))
	entry := topicEntryForTest()
	entry.ResumeThreadID = "a"
	if err := store.Put(entry); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(entry.SurfaceSessionID)
	got.ActorUserID = "ou_other_actor"
	if err := store.Put(got); err != nil {
		t.Fatal(err)
	}
	got.FeishuTopicOwnerID = "ou_new_owner"
	if err := store.Put(got); err == nil {
		t.Fatal("owner replacement accepted")
	}
	got.FeishuTopicOwnerID = "ou_owner"
	got.ResumeThreadID = "b"
	got.FeishuTopicBoundThreadID = "b"
	if err := store.Put(got); err == nil {
		t.Fatal("thread rebind accepted")
	}
	if err := store.ReplaceAll(map[string]Entry{got.SurfaceSessionID: got}); err == nil {
		t.Fatal("bulk thread rebind accepted")
	}
	persisted, _ := store.Get(entry.SurfaceSessionID)
	if persisted.FeishuTopicBoundThreadID != "a" || persisted.FeishuTopicOwnerID != "ou_owner" {
		t.Fatalf("rejected mutation leaked: %#v", persisted)
	}
}

func TestTopicResumeUnknownOwnerCannotBeInferredAndClosedNativeBindingIsImmutable(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), StateFileName))
	unknown := topicEntryForTest()
	unknown.FeishuTopicOwnerID = ""
	if err := store.Put(unknown); err == nil {
		t.Fatal("unknown inbound topic acquired its actor as owner")
	}
	entry := topicEntryForTest()
	entry.FeishuTopicID = "omt_native"
	entry.FeishuTopicClosed = true
	if err := store.Put(entry); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Entry){
		"native replacement": func(e *Entry) { e.FeishuTopicID = "omt_other" },
		"native deletion":    func(e *Entry) { e.FeishuTopicID = "" },
		"reopen":             func(e *Entry) { e.FeishuTopicClosed = false },
	} {
		t.Run(name, func(t *testing.T) {
			changed := entry
			mutate(&changed)
			if err := store.Put(changed); err == nil {
				t.Fatal("immutable topic update accepted")
			}
			if err := store.ReplaceAll(map[string]Entry{changed.SurfaceSessionID: changed}); err == nil {
				t.Fatal("bulk immutable topic update accepted")
			}
		})
	}
	got, _ := store.Get(entry.SurfaceSessionID)
	if got.FeishuTopicID != "omt_native" || !got.FeishuTopicClosed {
		t.Fatal("rejected update changed tombstone")
	}
}

func TestTopicResumeMessageIDsBoundedAndCopied(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), StateFileName))
	entry := topicEntryForTest()
	for i := 0; i < 600; i++ {
		entry.FeishuTopicMessageIDs = append(entry.FeishuTopicMessageIDs, fmt.Sprintf("om_%d", i))
	}
	entry.FeishuTopicMessageIDs = append(entry.FeishuTopicMessageIDs, "om_599", "om_root", " ")
	if err := store.Put(entry); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(entry.SurfaceSessionID)
	if len(got.FeishuTopicMessageIDs) != MaxTopicMessageIDs+1 || got.FeishuTopicMessageIDs[0] != "om_root" ||
		got.FeishuTopicMessageIDs[1] != "om_88" || got.FeishuTopicMessageIDs[512] != "om_599" {
		t.Fatalf("incorrect retained IDs: count=%d first=%q", len(got.FeishuTopicMessageIDs), got.FeishuTopicMessageIDs[0])
	}
	got.FeishuTopicMessageIDs[0] = "changed"
	entries := store.Entries()
	copy := entries[entry.SurfaceSessionID]
	copy.FeishuTopicMessageIDs[1] = "changed"
	again, _ := store.Get(entry.SurfaceSessionID)
	if again.FeishuTopicMessageIDs[0] != "om_root" || again.FeishuTopicMessageIDs[1] != "om_88" {
		t.Fatal("returned IDs alias store state")
	}
	if SameEntryContent(got, again) {
		t.Fatal("message ID mutation absent from equality")
	}
}

func TestTopicResumeWriteFailureDoesNotMutateMemoryOrDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFileName)
	store := NewStore(path)
	entry := topicEntryForTest()
	if err := store.Put(entry); err != nil {
		t.Fatal(err)
	}
	// Replacing the destination with a directory deterministically blocks atomic
	// file replacement on Windows and Unix, without relying on user permissions.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	entry.FeishuTopicTitle = "unsaved"
	if err := store.Put(entry); err == nil {
		t.Fatal("expected write failure")
	}
	got, _ := store.Get(entry.SurfaceSessionID)
	if got.FeishuTopicTitle != "测试话题" {
		t.Fatal("failed write replaced in-memory binding")
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatal("failed write changed destination")
	}
}

func TestTopicResumeLoadKeepsLegacyEntriesUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFileName)
	legacy := `{"version":1,"entries":{"feishu:bot:chat:oc_room":{"surfaceSessionID":"feishu:bot:chat:oc_room","chatID":"oc_room","actorUserID":"ou_owner","resumeThreadID":"legacy-thread"}}}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := store.Get("feishu:bot:chat:oc_room")
	if !ok || got.ResumeThreadID != "legacy-thread" || got.FeishuTopicOwnerID != "" || len(got.FeishuTopicMessageIDs) != 0 {
		t.Fatalf("legacy entry changed: %#v", got)
	}
}
