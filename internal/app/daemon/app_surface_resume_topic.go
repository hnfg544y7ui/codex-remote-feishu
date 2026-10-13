package daemon

import (
	"github.com/kxn/codex-remote-feishu/internal/app/daemon/surfaceresume"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
)

func copySurfaceTopicResumeMetadata(surface *state.SurfaceConsoleRecord, entry *surfaceresume.Entry) {
	entry.FeishuTopicID = surface.FeishuTopicID
	entry.FeishuTopicRootMessageID = surface.FeishuTopicRootMessageID
	entry.FeishuTopicTitle = surface.FeishuTopicTitle
	entry.FeishuTopicOwnerID = surface.FeishuTopicOwnerID
	entry.FeishuTopicBoundThreadID = surface.FeishuTopicBoundThreadID
	entry.FeishuTopicMessageURL = surface.FeishuTopicMessageURL
	entry.FeishuTopicMessageIDs = append([]string(nil), surface.FeishuTopicMessageIDs...)
	entry.FeishuTopicClosed = surface.FeishuTopicClosed
}

func (a *App) currentSurfaceResumeWorkspaceKeyLocked(surface *state.SurfaceConsoleRecord, snapshot *control.Snapshot) string {
	if ref, ok := feishuidentity.ParseSurfaceRef(surface.SurfaceSessionID); ok && ref.IsTopic() {
		key := state.ResolveWorkspaceClaimKey(surface.ClaimedWorkspaceKey, surface.PreparedThreadCWD)
		if inst := a.service.Instance(surface.AttachedInstanceID); key == "" && inst != nil {
			key = state.ResolveWorkspaceClaimKey(inst.WorkspaceKey, inst.WorkspaceRoot)
		}
		return key
	}
	if snapshot != nil {
		return state.ResolveWorkspaceClaimKey(snapshot.WorkspaceKey)
	}
	return ""
}
