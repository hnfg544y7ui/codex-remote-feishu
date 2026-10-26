package surfaceresume

import (
	"fmt"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
)

// RebindTopic is the explicit compare-and-swap path for an authorized idle topic
// switch. Callers serialize store access, just as for Put and ReplaceAll.
// Ordinary writes retain their established-binding immutability checks.
func (s *Store) RebindTopic(expectedThreadID string, entry Entry) error {
	if s == nil || s.Store == nil || strings.TrimSpace(s.Path()) == "" {
		return fmt.Errorf("topic persistence is unavailable")
	}
	expectedThreadID = strings.TrimSpace(expectedThreadID)
	next, valid := NormalizeEntry(entry)
	if !valid || expectedThreadID == "" || strings.TrimSpace(entry.FeishuTopicBoundThreadID) == "" || next.FeishuTopicBoundThreadID == "" ||
		next.ResumeThreadID != next.FeishuTopicBoundThreadID || next.FeishuTopicClosed {
		return fmt.Errorf("topic rebind requires an open topic and a nonempty matching target")
	}
	ref, topic := feishuidentity.ParseSurfaceRef(next.SurfaceSessionID)
	if !topic || !ref.IsTopic() {
		return fmt.Errorf("topic rebind requires a topic surface")
	}
	entries := s.Entries()
	previous, exists := entries[next.SurfaceSessionID]
	if !exists || previous.FeishuTopicBoundThreadID == "" || previous.FeishuTopicBoundThreadID != expectedThreadID {
		return fmt.Errorf("topic binding changed before rebind")
	}
	if previous.FeishuTopicClosed || previous.GatewayID != next.GatewayID || previous.ChatID != next.ChatID ||
		previous.FeishuTopicOwnerID != next.FeishuTopicOwnerID || previous.FeishuTopicID != next.FeishuTopicID ||
		previous.FeishuTopicRootMessageID != next.FeishuTopicRootMessageID {
		return fmt.Errorf("topic rebind cannot change topic identity or closed state")
	}
	if previous.ProductMode != next.ProductMode || previous.Backend != next.Backend ||
		previous.CodexProfileID != next.CodexProfileID || previous.ClaudeProfileID != next.ClaudeProfileID ||
		previous.OpenCodeProfileID != next.OpenCodeProfileID {
		return fmt.Errorf("topic rebind cannot change backend or profile selection")
	}
	entries[next.SurfaceSessionID] = next
	if canonical, changed := CanonicalizeEntries(entries); changed {
		entries = canonical
	}
	return s.Replace(entries)
}
