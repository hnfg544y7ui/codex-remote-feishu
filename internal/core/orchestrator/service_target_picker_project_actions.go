package orchestrator

import (
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/frontstagecontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func (s *Service) rebuildDesktopProjectPicker(surface *state.SurfaceConsoleRecord, record *activeTargetPickerRecord) []eventcontract.Event {
	view, err := s.buildTargetPickerView(surface, record)
	if err != nil {
		return notice(surface, "target_picker_unavailable", err.Error())
	}
	return []eventcontract.Event{s.targetPickerViewEvent(surface, view, true)}
}

func (s *Service) handleDesktopProjectPickerPage(surface *state.SurfaceConsoleRecord, record *activeTargetPickerRecord, fieldName string, cursor int) []eventcontract.Event {
	view, err := s.buildTargetPickerView(surface, record)
	if err != nil {
		return notice(surface, "target_picker_unavailable", err.Error())
	}
	switch strings.TrimSpace(fieldName) {
	case frontstagecontract.CardTargetPickerWorkspaceFieldName:
		record.WorkspaceCursor = normalizePickerDropdownCursor(cursor, len(view.WorkspaceOptions))
		if len(view.WorkspaceOptions) != 0 {
			record.SelectedProjectID, _ = desktopProjectIDFromValue(view.WorkspaceOptions[record.WorkspaceCursor].Value)
		}
		record.SelectedWorkspaceKey = ""
		record.SelectedSessionValue = targetPickerAutoSession
		record.SessionCursor = -1
	case frontstagecontract.CardTargetPickerSessionFieldName:
		record.SessionCursor = normalizePickerDropdownCursor(cursor, len(view.SessionOptions))
		record.SelectedSessionValue = ""
	default:
		return notice(surface, "target_picker_invalid_page_action", "当前翻页动作无效，请重新打开项目选择器。")
	}
	return s.rebuildDesktopProjectPicker(surface, record)
}
