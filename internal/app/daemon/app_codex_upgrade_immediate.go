package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/codexupgrade"
	"github.com/kxn/codex-remote-feishu/internal/app/daemon/codexupgraderuntime"
	"github.com/kxn/codex-remote-feishu/internal/app/sharedcodex"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

const (
	codexUpgradeOwnerActionImmediate        = "immediate"
	codexUpgradeOwnerActionImmediateConfirm = "confirm_immediate"
	codexUpgradeOwnerActionImmediateCancel  = "cancel_immediate"
)

func (a *App) previewImmediateCodexUpgradeOwnerFlowLocked(command control.DaemonCommand) []eventcontract.Event {
	flow, blocked := a.requireCodexUpgradeOwnerFlowLocked(command.SurfaceSessionID, command.PickerID, a.service.SurfaceActorUserID(command.SurfaceSessionID))
	if blocked != nil {
		return blocked
	}
	if a.codexUpgradeRuntime.Active != nil || len(codexUpgradeOwnerImmediateButtons(flow)) == 0 {
		return []eventcontract.Event{upgradeNoticeEvent(command.SurfaceSessionID, "codex_upgrade_owner_invalid_stage", "当前不能预览立即升级，请先重新检查更新。")}
	}
	// Each confirmation gets a fresh identity. A previous card must never
	// authorize a newer preview, even when the target version is unchanged.
	flow.FlowID = a.nextCodexUpgradeOwnerFlowIDLocked()
	flow.Immediate, flow.Rollback, flow.ImmediatePlan = true, false, nil
	a.refreshCodexUpgradeOwnerFlowLocked(flow, codexupgraderuntime.OwnerFlowStageChecking)
	go a.runImmediateCodexUpgradePreview(flow.FlowID, flow.SurfaceSessionID)
	return []eventcontract.Event{codexUpgradeOwnerImmediatePreviewEvent(command.SurfaceSessionID, flow)}
}

func (a *App) runImmediateCodexUpgradePreview(flowID, surfaceID string) {
	a.mu.Lock()
	flow := a.activeCodexUpgradeOwnerFlowLocked()
	if !immediatePreviewMatches(flow, flowID, surfaceID) {
		a.mu.Unlock()
		return
	}
	current, target := flow.CurrentVersion, flow.TargetVersion
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	owner, err := a.sharedCodexOwner()
	var plan sharedcodex.ImmediatePlan
	if err == nil {
		if preview, ok := owner.(sharedcodex.ImmediateMaintenance); ok {
			plan, err = preview.PreviewImmediate(ctx, sharedcodex.Upgrade{Version: target, ExpectedVersion: current})
		} else {
			err = fmt.Errorf("共享后端宿主尚不支持立即升级，请先更新宿主")
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	flow = a.activeCodexUpgradeOwnerFlowLocked()
	if a.shuttingDown || !immediatePreviewMatches(flow, flowID, surfaceID) {
		return
	}
	if err == nil && (plan.Version != current || plan.TargetVersion != target || !plan.ExpiresAt.After(time.Now()) || plan.Token == "") {
		err = fmt.Errorf("立即升级影响预览已变化或过期，请重新检查")
	}
	var event eventcontract.Event
	if err != nil {
		flow.Immediate, flow.ImmediatePlan = false, nil
		a.refreshCodexUpgradeOwnerFlowLocked(flow, codexupgraderuntime.OwnerFlowStageReady)
		event = codexUpgradeOwnerStableEvent(surfaceID, flow, fmt.Sprintf("读取立即升级影响失败：%v；未执行升级。", err))
	} else {
		flow.ImmediatePlan = &plan
		a.refreshCodexUpgradeOwnerFlowLocked(flow, codexupgraderuntime.OwnerFlowStageImmediateConfirm)
		event = codexUpgradeOwnerImmediateConfirmEvent(surfaceID, flow)
	}
	a.handleUIEventsLocked(context.Background(), []eventcontract.Event{event})
}

func immediatePreviewMatches(flow *codexupgraderuntime.OwnerCardFlowRecord, flowID, surfaceID string) bool {
	return flow != nil && flow.FlowID == flowID && flow.SurfaceSessionID == surfaceID && flow.Immediate && flow.Stage == codexupgraderuntime.OwnerFlowStageChecking
}

func (a *App) confirmImmediateCodexUpgradeOwnerFlowLocked(command control.DaemonCommand) []eventcontract.Event {
	flow, blocked := a.requireCodexUpgradeOwnerFlowLocked(command.SurfaceSessionID, command.PickerID, a.service.SurfaceActorUserID(command.SurfaceSessionID))
	if blocked != nil {
		return blocked
	}
	if a.codexUpgradeRuntime.Active != nil || flow.Stage != codexupgraderuntime.OwnerFlowStageImmediateConfirm ||
		!flow.Immediate || !flow.ImmediateSupported || flow.SourceKind != codexupgrade.SourceSharedBackend || flow.ImmediatePlan == nil {
		return []eventcontract.Event{upgradeNoticeEvent(command.SurfaceSessionID, "codex_upgrade_owner_invalid_stage", "当前不能确认立即升级，请重新读取影响预览。")}
	}
	plan := flow.ImmediatePlan
	if !plan.ExpiresAt.After(time.Now()) || plan.Version != flow.CurrentVersion || plan.TargetVersion != flow.TargetVersion {
		a.cancelImmediateCodexUpgradeFlowLocked(flow)
		return []eventcontract.Event{codexUpgradeOwnerStableEvent(command.SurfaceSessionID, flow, "立即升级预览已过期或变化，未执行升级。请重新点击“立即升级”。")}
	}
	a.refreshCodexUpgradeOwnerFlowLocked(flow, codexupgraderuntime.OwnerFlowStageChecking)
	go a.runCodexUpgradeOwnerConfirm(flow.FlowID, command.SurfaceSessionID)
	return []eventcontract.Event{codexUpgradeOwnerCardEvent(command.SurfaceSessionID, flow, "正在准备立即升级", "progress",
		codexUpgradeOwnerContextSections(flow), codexUpgradeOwnerNoticeSections("正在重新校验版本、确认影响范围并准备升级文件；确认范围变化时将停止升级。"), nil, false)}
}

func (a *App) cancelImmediateCodexUpgradeFlowLocked(flow *codexupgraderuntime.OwnerCardFlowRecord) {
	flow.Immediate, flow.ImmediatePlan = false, nil
	flow.FlowID = a.nextCodexUpgradeOwnerFlowIDLocked()
	a.refreshCodexUpgradeOwnerFlowLocked(flow, codexupgraderuntime.OwnerFlowStageReady)
}

func (a *App) cancelImmediateCodexUpgradeOwnerFlowLocked(command control.DaemonCommand) []eventcontract.Event {
	flow, blocked := a.requireCodexUpgradeOwnerFlowLocked(command.SurfaceSessionID, command.PickerID, a.service.SurfaceActorUserID(command.SurfaceSessionID))
	if blocked != nil {
		return blocked
	}
	if a.codexUpgradeRuntime.Active != nil || flow.Stage != codexupgraderuntime.OwnerFlowStageImmediateConfirm {
		return []eventcontract.Event{upgradeNoticeEvent(command.SurfaceSessionID, "codex_upgrade_owner_invalid_stage", "当前操作正在处理中，请等待现有卡片更新。")}
	}
	a.cancelImmediateCodexUpgradeFlowLocked(flow)
	return []eventcontract.Event{codexUpgradeOwnerStableEvent(command.SurfaceSessionID, flow, "已取消立即升级，未停止任务。")}
}
