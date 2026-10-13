package daemon

import (
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/app/daemon/surfaceresume"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
)

func surfaceResumeEntryAllowsBackgroundRecovery(entry surfaceresume.Entry) bool {
	return !entry.FeishuTopicClosed && !surfaceResumeEntryIsFeishuGroup(entry)
}

func surfaceAllowsDaemonLifecycleNotice(surface *state.SurfaceConsoleRecord) bool {
	return !surfaceIsFeishuGroup(strings.TrimSpace(surfaceIDFromSurface(surface)))
}

func surfaceResumeEntryAllowsOnDemandRecovery(entry surfaceresume.Entry, action control.Action) bool {
	if entry.FeishuTopicClosed || !surfaceResumeEntryIsFeishuGroup(entry) ||
		strings.TrimSpace(action.SurfaceSessionID) != strings.TrimSpace(entry.SurfaceSessionID) ||
		!surfaceAllowsOnDemandRecoveryAction(entry.SurfaceSessionID, action.Kind) {
		return false
	}
	if ref, ok := feishuidentity.ParseSurfaceRef(entry.SurfaceSessionID); ok && ref.IsTopic() {
		return strings.TrimSpace(entry.FeishuTopicID) != "" && strings.TrimSpace(entry.FeishuTopicOwnerID) != "" &&
			strings.TrimSpace(entry.FeishuTopicBoundThreadID) != "" && entry.ResumeThreadID == entry.FeishuTopicBoundThreadID
	}
	return true
}

func surfaceAllowsOnDemandRecoveryAction(surfaceID string, kind control.ActionKind) bool {
	if kind == control.ActionTextMessage {
		return true
	}
	ref, ok := feishuidentity.ParseSurfaceRef(surfaceID)
	return ok && ref.IsTopic() && kind == control.ActionShowHistory
}

func surfaceResumeEntryIsFeishuGroup(entry surfaceresume.Entry) bool {
	return surfaceIsFeishuGroup(strings.TrimSpace(entry.SurfaceSessionID))
}

func surfaceIDFromSurface(surface *state.SurfaceConsoleRecord) string {
	if surface == nil {
		return ""
	}
	return surface.SurfaceSessionID
}

func surfaceIsFeishuGroup(surfaceID string) bool {
	ref, ok := feishuidentity.ParseSurfaceRef(surfaceID)
	return ok && ref.IsGroup()
}

func surfaceIsFeishuRoom(surfaceID string) bool {
	ref, ok := feishuidentity.ParseSurfaceRef(surfaceID)
	return ok && ref.IsChat()
}
