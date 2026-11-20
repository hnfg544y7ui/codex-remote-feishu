package orchestrator

import (
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

type commandMenuStage string

const (
	commandMenuStageDetached      commandMenuStage = commandMenuStage(control.FeishuCommandMenuStageDetached)
	commandMenuStageNormalWorking commandMenuStage = commandMenuStage(control.FeishuCommandMenuStageNormalWorking)
	commandMenuStageVSCodeWorking commandMenuStage = commandMenuStage(control.FeishuCommandMenuStageVSCodeWorking)
)

func parseCommandMenuView(raw string) string {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) < 2 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(fields[1]))
}

func (s *Service) commandMenuStage(surface *state.SurfaceConsoleRecord) commandMenuStage {
	if surface == nil || strings.TrimSpace(surface.AttachedInstanceID) == "" {
		return commandMenuStageDetached
	}
	if s.surfaceIsVSCode(surface) {
		return commandMenuStageVSCodeWorking
	}
	return commandMenuStageNormalWorking
}

func (s *Service) buildCommandHelpView(surface *state.SurfaceConsoleRecord) control.FeishuCatalogView {
	page := control.BuildFeishuCommandDisplayPageViewForContext(
		"命令帮助",
		"按当前模式列出可用命令和用法。",
		false,
		s.buildCatalogContext(surface),
	)
	page.CommandID = control.FeishuCommandHelp
	return control.FeishuCatalogView{Page: &page}
}
