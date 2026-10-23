package daemon

import (
	"context"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

func (a *App) maybeHandleStandaloneCodexUpgradeActionLocked(ctx context.Context, action control.Action) bool {
	tx := a.codexUpgradeRuntime.Active
	if tx == nil || codexUpgradeAllowsAction(action) {
		return false
	}
	a.ensureSurfaceRouteForNotice(action)
	if !a.standaloneCodexUpgradeAffectsSurfaceIDLocked(action.SurfaceSessionID) {
		return false
	}

	if strings.TrimSpace(action.SurfaceSessionID) == strings.TrimSpace(tx.InitiatorSurface) {
		a.handleUIEventsLocked(ctx, codexUpgradeBlockedEvents(action.SurfaceSessionID, false, tx.RecoveryRequired))
		a.syncSurfaceResumeStateLocked(nil)
		a.syncClaudeWorkspaceProfileStateLocked()
		return true
	}
	if codexUpgradeQueueableInput(action) && strings.TrimSpace(a.service.AttachedInstanceID(action.SurfaceSessionID)) != "" {
		a.service.HoldSurfaceDispatch(action.SurfaceSessionID)
		tx.PausedSurfaceIDs[action.SurfaceSessionID] = true
		a.service.PauseSurfaceDispatch(action.SurfaceSessionID)
		events := a.applyIngressActionLocked(action)
		events = append(events, codexUpgradeBlockedEvents(action.SurfaceSessionID, true, tx.RecoveryRequired)...)
		a.handleUIEventsLocked(ctx, events)
		a.syncSurfaceResumeStateLocked(nil)
		a.syncClaudeWorkspaceProfileStateLocked()
		return true
	}

	a.handleUIEventsLocked(ctx, codexUpgradeBlockedEvents(action.SurfaceSessionID, false, tx.RecoveryRequired))
	a.syncSurfaceResumeStateLocked(nil)
	a.syncClaudeWorkspaceProfileStateLocked()
	return true
}

func codexUpgradeAllowsAction(action control.Action) bool {
	switch action.Kind {
	case control.ActionStatus,
		control.ActionUpgradeCommand,
		control.ActionUpgradeOwnerFlow,
		control.ActionDebugCommand,
		control.ActionReactionCreated,
		control.ActionMessageRecalled:
		return true
	default:
		return false
	}
}

func codexUpgradeQueueableInput(action control.Action) bool {
	switch action.Kind {
	case control.ActionTextMessage,
		control.ActionImageMessage,
		control.ActionFileMessage:
		return true
	default:
		return false
	}
}

func codexUpgradeBlockedEvents(surfaceID string, queued, recoveryRequired bool) []eventcontract.Event {
	code := "codex_upgrade_running"
	text := "当前正在升级 Codex。请等待完成后再继续操作。"
	if queued {
		text = "当前正在升级 Codex，这条输入会在升级完成后执行。"
	}
	if recoveryRequired {
		code = "codex_shared_backend_recovery"
		text = "共享后端维护需要恢复，继续等待不会自动解除。请先修复宿主，再重启 Remote 恢复会话连接；可用 `/upgrade codex` 查看恢复提示。"
		if queued {
			text += " 这条消息已缓存。"
		}
	}
	return []eventcontract.Event{{
		Kind:             eventcontract.KindNotice,
		SurfaceSessionID: strings.TrimSpace(surfaceID),
		Notice: &control.Notice{
			Code: code,
			Text: text,
		},
	}}
}
