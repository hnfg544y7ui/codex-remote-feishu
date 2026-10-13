package surfaceresume

import (
	"slices"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
)

const MaxTopicMessageIDs = 512

func normalizeTopicEntry(entry Entry) (Entry, bool) {
	ref, parsed := feishuidentity.ParseSurfaceRef(entry.SurfaceSessionID)
	if !parsed || !ref.IsTopic() {
		// Legacy surfaces have no topic-owned metadata.
		if entry.FeishuTopicID != "" || entry.FeishuTopicRootMessageID != "" || entry.FeishuTopicOwnerID != "" ||
			entry.FeishuTopicBoundThreadID != "" || entry.FeishuTopicTitle != "" || entry.FeishuTopicMessageURL != "" ||
			len(entry.FeishuTopicMessageIDs) != 0 || entry.FeishuTopicClosed {
			return Entry{}, false
		}
		return entry, true
	}
	entry.FeishuTopicID = strings.TrimSpace(entry.FeishuTopicID)
	entry.FeishuTopicRootMessageID = strings.TrimSpace(entry.FeishuTopicRootMessageID)
	entry.FeishuTopicTitle = strings.TrimSpace(entry.FeishuTopicTitle)
	entry.FeishuTopicOwnerID = strings.TrimSpace(entry.FeishuTopicOwnerID)
	entry.FeishuTopicBoundThreadID = strings.TrimSpace(entry.FeishuTopicBoundThreadID)
	entry.FeishuTopicMessageURL = strings.TrimSpace(entry.FeishuTopicMessageURL)
	if (entry.GatewayID != "" && entry.GatewayID != ref.GatewayID) ||
		(entry.ChatID != "" && entry.ChatID != ref.TopicChatID()) ||
		(entry.FeishuTopicRootMessageID != "" && entry.FeishuTopicRootMessageID != ref.TopicRootMessageID()) {
		return Entry{}, false
	}
	entry.GatewayID = ref.GatewayID
	entry.ChatID = ref.TopicChatID()
	entry.FeishuTopicRootMessageID = ref.TopicRootMessageID()
	if entry.FeishuTopicOwnerID == "" {
		return Entry{}, false
	}
	if entry.FeishuTopicBoundThreadID == "" {
		entry.FeishuTopicBoundThreadID = entry.ResumeThreadID
	}
	if entry.ResumeThreadID != "" && entry.ResumeThreadID != entry.FeishuTopicBoundThreadID {
		return Entry{}, false
	}
	entry.FeishuTopicMessageIDs = normalizeTopicMessageIDs(entry.FeishuTopicRootMessageID, entry.FeishuTopicMessageIDs)
	return entry, true
}

func normalizeTopicMessageIDs(root string, ids []string) []string {
	// Keep the newest occurrence, while retaining the immutable root separately.
	seen := map[string]bool{root: true}
	latest := make([]string, 0, min(len(ids), MaxTopicMessageIDs))
	for i := len(ids) - 1; i >= 0 && len(latest) < MaxTopicMessageIDs; i-- {
		id := strings.TrimSpace(ids[i])
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		latest = append(latest, id)
	}
	slices.Reverse(latest)
	return append([]string{root}, latest...)
}

func sameTopicEntry(left, right Entry) bool {
	return left.FeishuTopicID == right.FeishuTopicID &&
		left.FeishuTopicRootMessageID == right.FeishuTopicRootMessageID &&
		left.FeishuTopicTitle == right.FeishuTopicTitle &&
		left.FeishuTopicOwnerID == right.FeishuTopicOwnerID &&
		left.FeishuTopicBoundThreadID == right.FeishuTopicBoundThreadID &&
		left.FeishuTopicMessageURL == right.FeishuTopicMessageURL &&
		left.FeishuTopicClosed == right.FeishuTopicClosed &&
		slices.Equal(left.FeishuTopicMessageIDs, right.FeishuTopicMessageIDs)
}

func sameTopicBinding(previous, current Entry) bool {
	ref, ok := feishuidentity.ParseSurfaceRef(previous.SurfaceSessionID)
	if !ok || !ref.IsTopic() {
		return true
	}
	return previous.FeishuTopicOwnerID == current.FeishuTopicOwnerID &&
		(previous.FeishuTopicBoundThreadID == "" || previous.FeishuTopicBoundThreadID == current.FeishuTopicBoundThreadID) &&
		(previous.FeishuTopicID == "" || previous.FeishuTopicID == current.FeishuTopicID) &&
		(!previous.FeishuTopicClosed || current.FeishuTopicClosed)
}

func cloneEntry(entry Entry) Entry {
	entry.FeishuTopicMessageIDs = slices.Clone(entry.FeishuTopicMessageIDs)
	if entry.CodexAdmissionRef != nil {
		ref := *entry.CodexAdmissionRef
		entry.CodexAdmissionRef = &ref
	}
	if entry.OpenCodeAdmissionRef != nil {
		ref := *entry.OpenCodeAdmissionRef
		entry.OpenCodeAdmissionRef = &ref
	}
	return entry
}
