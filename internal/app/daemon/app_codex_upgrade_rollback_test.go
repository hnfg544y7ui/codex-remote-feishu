package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/codexupgrade"
	"github.com/kxn/codex-remote-feishu/internal/app/daemon/codexupgraderuntime"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
)

func TestCodexRollbackOwnerCardNeedsSuccessfulManagedCheck(t *testing.T) {
	app, _ := newUpgradeTestApp(t, newLifecycleGateway())
	app.service.MaterializeSurface("surface-1", "", "chat-1", "owner")
	flow := app.newCodexUpgradeOwnerFlowLocked("surface-1", "owner", "")
	flow.SourceKind = codexupgrade.SourceManagedRuntime
	flow.PreviousVersion = "0.133.0"
	if buttonsContainRollback(codexUpgradeOwnerReadyButtons(flow)) {
		t.Fatal("unchecked card offered rollback")
	}
	check := codexUpgradeCheckResult{
		Installation:   codexupgrade.Installation{SourceKind: codexupgrade.SourceManagedRuntime, PackageVersion: "0.134.0", ManagedPreviousVersion: "0.133.0"},
		CurrentVersion: "0.134.0", LatestVersion: "0.134.0", PreviousVersion: "0.133.0", CanRollback: true,
	}
	events := app.finishCodexUpgradeOwnerCheckLocked("surface-1", flow.FlowID, check, "")
	if len(events) != 1 || flow.HasUpdate {
		t.Fatalf("expected latest managed version with rollback: %#v", events)
	}
	if !buttonsContainRollback(codexUpgradeOwnerReadyButtons(flow)) {
		t.Fatal("latest managed version did not offer rollback")
	}
	sections := codexUpgradeOwnerContextSections(flow)
	if len(sections) < 3 {
		t.Fatalf("missing current, previous or source context: %#v", sections)
	}
	flow.SourceKind = codexupgrade.SourceStandaloneNPM
	if buttonsContainRollback(codexUpgradeOwnerReadyButtons(flow)) {
		t.Fatal("external npm installation offered managed rollback")
	}
	flow.SourceKind = codexupgrade.SourceManagedRuntime
	flow.PreviousVersion = ""
	if buttonsContainRollback(codexUpgradeOwnerReadyButtons(flow)) {
		t.Fatal("missing previous release offered rollback")
	}
}

func TestCodexRollbackOwnerRejectsStaleUnauthorizedAndConcurrentActions(t *testing.T) {
	for _, scenario := range []string{"expired", "old-flow", "other-owner", "checking", "running", "active-transaction", "unchecked", "external", "same-version"} {
		t.Run(scenario, func(t *testing.T) {
			app, _ := newUpgradeTestApp(t, newLifecycleGateway())
			app.service.MaterializeSurface("surface-1", "", "chat-1", "owner")
			flow := app.newCodexUpgradeOwnerFlowLocked("surface-1", "owner", "")
			flow.SourceKind = codexupgrade.SourceManagedRuntime
			flow.CurrentVersion, flow.PreviousVersion = "0.134.0", "0.133.0"
			flow.Checked, flow.CanRollback = true, true
			flow.Stage = codexupgraderuntime.OwnerFlowStageReady
			command := control.DaemonCommand{SurfaceSessionID: "surface-1", PickerID: flow.FlowID, OptionID: "rollback"}
			switch scenario {
			case "expired":
				flow.ExpiresAt = time.Now().Add(-time.Second)
			case "old-flow":
				command.PickerID = "codex-upgrade-owner-old"
			case "other-owner":
				flow.OwnerUserID = "different-owner"
			case "checking":
				flow.Stage = codexupgraderuntime.OwnerFlowStageChecking
			case "running":
				flow.Stage = codexupgraderuntime.OwnerFlowStageRunning
			case "active-transaction":
				app.codexUpgradeRuntime.Active = &codexupgraderuntime.Transaction{ID: "other"}
			case "unchecked":
				flow.Checked = false
			case "external":
				flow.SourceKind = codexupgrade.SourceStandaloneNPM
			case "same-version":
				flow.PreviousVersion = flow.CurrentVersion
			}
			events := app.handleCodexUpgradeOwnerFlowCommandLocked(command)
			if len(events) != 1 || events[0].Notice == nil {
				t.Fatalf("unsafe callback did not produce a rejection notice: %#v", events)
			}
			if flow.Rollback || flow.TargetVersion != "" {
				t.Fatalf("rejected callback changed target: %#v", flow)
			}
			if scenario == "other-owner" && events[0].Notice.Code != "codex_upgrade_owner_unauthorized" {
				t.Fatalf("owner gate not used: %#v", events[0].Notice)
			}
			if (scenario == "expired" || scenario == "old-flow") && events[0].Notice.Code != "codex_upgrade_owner_expired" {
				t.Fatalf("freshness gate not used: %#v", events[0].Notice)
			}
		})
	}
}

func TestCodexRollbackOwnerTerminalUsesExactPreviousVersion(t *testing.T) {
	app, _ := newUpgradeTestApp(t, newLifecycleGateway())
	app.service.MaterializeSurface("surface-1", "", "chat-1", "owner")
	flow := app.newCodexUpgradeOwnerFlowLocked("surface-1", "owner", "")
	flow.SourceKind = codexupgrade.SourceManagedRuntime
	flow.CurrentVersion, flow.PreviousVersion = "0.134.0", "0.133.0"
	flow.TargetVersion, flow.LatestVersion = "0.133.0", "0.135.0"
	flow.Rollback = true
	title, preparation := codexUpgradeOwnerRunningText(flow)
	if title != "正在回退 Codex" || !strings.Contains(preparation, "上一版本") {
		t.Fatalf("wrong rollback progress: %s %s", title, preparation)
	}
	events := app.finishCodexUpgradeOwnerRunLocked("surface-1", flow.FlowID, nil)
	if len(events) != 1 || flow.CurrentVersion != "0.133.0" || flow.PreviousVersion != "0.134.0" {
		t.Fatalf("rollback used latest instead of previous: %#v %#v", flow, events)
	}
	if app.codexUpgradeRuntime.ActiveFlow != nil {
		t.Fatal("terminal owner flow stayed actionable")
	}
	if codexUpgradeOwnerTerminalTitle(flow, true) != "Codex 回退完成" {
		t.Fatal("rollback rendered as upgrade")
	}
}

func buttonsContainRollback(buttons []control.CommandCatalogButton) bool {
	for _, button := range buttons {
		if button.Label == "回退上一版" && button.CallbackValue[upgradeOwnerPayloadOptionKey] == "rollback" {
			return true
		}
	}
	return false
}
