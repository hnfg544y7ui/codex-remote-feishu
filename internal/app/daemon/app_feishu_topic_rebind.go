package daemon

import (
	"fmt"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func topicSwitchPending(surface *state.SurfaceConsoleRecord) bool {
	return surface != nil && surface.FeishuTopicRootMessageID != "" && surface.FeishuTopicBoundThreadID != "" &&
		surface.PendingHeadless != nil && surface.PendingHeadless.ThreadID != "" &&
		surface.PendingHeadless.ThreadID != surface.FeishuTopicBoundThreadID
}

func (a *App) committedPendingTopicSwitchTargetLocked(surface *state.SurfaceConsoleRecord) (surfaceResumeTarget, string, bool) {
	// A switch stays speculative even when its previous worker was reclaimed.
	// Pending launch metadata cannot replace the last durably committed target.
	if !topicSwitchPending(surface) || a.surfaceResumeRuntime.store == nil {
		return surfaceResumeTarget{}, "", false
	}
	previous, ok := a.surfaceResumeRuntime.store.Get(surface.SurfaceSessionID)
	if !ok || previous.FeishuTopicBoundThreadID != surface.FeishuTopicBoundThreadID || previous.ResumeThreadID != surface.FeishuTopicBoundThreadID {
		return surfaceResumeTarget{}, "", false
	}
	return surfaceResumeTarget{
		ResumeInstanceID: previous.ResumeInstanceID, ResumeThreadID: previous.ResumeThreadID,
		ResumeThreadTitle: previous.ResumeThreadTitle, ResumeThreadCWD: previous.ResumeThreadCWD,
		ResumeWorkspaceKey: previous.ResumeWorkspaceKey, ResumeRouteMode: previous.ResumeRouteMode,
		ResumeHeadless: previous.ResumeHeadless,
	}, previous.ResumeWorkspaceKey, true
}

func (a *App) surfaceResumeProjectionSnapshotLocked(surface *state.SurfaceConsoleRecord) *control.Snapshot {
	// SurfaceSnapshot normalizes live state. A topic projection may instead be
	// the candidate for an uncommitted transaction, so derive it without this call.
	if surface.FeishuTopicRootMessageID != "" {
		return nil
	}
	return a.service.SurfaceSnapshot(surface.SurfaceSessionID)
}

func topicResumeTargetHeadless(surface *state.SurfaceConsoleRecord, instance *state.InstanceRecord, fallback bool) bool {
	if surface.FeishuTopicRootMessageID == "" {
		return fallback
	}
	return instance.Managed && state.IsInstanceSource(instance.Source, state.InstanceSourceHeadless)
}

// CommitTopicThreadRebind runs under App.mu, before the orchestrator changes any
// live route or releases its old claims. A failed write therefore keeps both
// the live binding and the durable resume target untouched.
func (a *App) CommitTopicThreadRebind(expectedThreadID string, next *state.SurfaceConsoleRecord) error {
	if next == nil || a.surfaceResumeRuntime.store == nil || !a.surfaceResumeRuntime.writable() {
		return fmt.Errorf("topic persistence is unavailable")
	}
	expectedThreadID = strings.TrimSpace(expectedThreadID)
	previous := a.topicSurfaceLocked(next.SurfaceSessionID)
	if previous == nil || expectedThreadID == "" || previous.FeishuTopicBoundThreadID != expectedThreadID ||
		previous.FeishuTopicClosed || next.FeishuTopicClosed || next.FeishuTopicBoundThreadID == "" ||
		next.SelectedThreadID != next.FeishuTopicBoundThreadID || next.RouteMode != state.RouteModePinned || next.PendingHeadless != nil {
		return fmt.Errorf("topic rebind requires the current open binding and a pinned target")
	}
	if previous.GatewayID != next.GatewayID || previous.ChatID != next.ChatID ||
		previous.FeishuTopicID != next.FeishuTopicID || previous.FeishuTopicOwnerID != next.FeishuTopicOwnerID ||
		previous.FeishuTopicRootMessageID != next.FeishuTopicRootMessageID {
		return fmt.Errorf("topic rebind cannot change topic identity")
	}
	instance := a.service.Instance(next.AttachedInstanceID)
	if instance == nil || instance.Threads[next.SelectedThreadID] == nil {
		return fmt.Errorf("topic rebind target is unavailable")
	}
	entry, ok := a.currentSurfaceResumeEntryLocked(next, false)
	if !ok {
		return fmt.Errorf("topic rebind failed validation")
	}
	if expectedThreadID != entry.ResumeThreadID {
		entry.CodexAdmissionRef = nil
		entry.OpenCodeAdmissionRef = nil
	}
	if err := a.surfaceResumeRuntime.store.RebindTopic(expectedThreadID, entry); err != nil {
		return fmt.Errorf("persist topic rebind: %w", err)
	}
	a.refreshTopicRoutingIndexLocked()
	return nil
}
