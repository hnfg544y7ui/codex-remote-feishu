package orchestrator

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/core/threadcatalogcontract"
)

const desktopProjectValuePrefix = "desktop-project:"
const desktopProjectlessID = "__unassigned__"

type desktopProjectIndex struct {
	snapshot   threadcatalogcontract.DesktopProjectSnapshot
	roots      map[string][]string
	byRoot     map[string]string
	projectIDs map[string]bool
}

func newDesktopProjectIndex(snapshot threadcatalogcontract.DesktopProjectSnapshot) *desktopProjectIndex {
	index := &desktopProjectIndex{snapshot: snapshot, roots: map[string][]string{}, byRoot: map[string]string{}, projectIDs: map[string]bool{}}
	for _, project := range snapshot.Projects {
		index.projectIDs[project.ID] = true
		for _, raw := range project.RootPaths {
			if root := normalizeWorkspaceClaimKey(raw); root != "" {
				index.roots[project.ID] = append(index.roots[project.ID], root)
			}
		}
	}
	return index
}

func (i *desktopProjectIndex) projectForWorkspace(key string) string {
	if key == "" {
		return desktopProjectlessID
	}
	if id, ok := i.byRoot[key]; ok {
		return id
	}
	compare := func(path string) string {
		path = strings.TrimRight(strings.ReplaceAll(path, "\\", "/"), "/")
		if runtime.GOOS == "windows" {
			path = strings.ToLower(path)
		}
		return path
	}
	path, best, id := compare(key), -1, desktopProjectlessID
	// Stable project order resolves overlapping identical roots deterministically.
	for _, project := range i.snapshot.Projects {
		for _, root := range i.roots[project.ID] {
			root = compare(root)
			if (path == root || strings.HasPrefix(path, root+"/")) && len(root) > best {
				best, id = len(root), project.ID
			}
		}
	}
	i.byRoot[key] = id
	return id
}

func (i *desktopProjectIndex) projectForThread(view *mergedThreadView) string {
	if id, explicit := i.snapshot.ThreadProjectIDs[view.ThreadID]; explicit {
		if id == "" || !i.projectIDs[id] {
			return desktopProjectlessID
		}
		return id
	}
	return i.projectForWorkspace(mergedThreadWorkspaceClaimKey(view))
}

func desktopProjectIDFromValue(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, desktopProjectValuePrefix) {
		return "", false
	}
	return strings.TrimPrefix(value, desktopProjectValuePrefix), true
}

func (s *Service) desktopProjectPickerSnapshot(surface *state.SurfaceConsoleRecord, record *activeTargetPickerRecord) (threadcatalogcontract.DesktopProjectSnapshot, bool, error) {
	switch record.Source {
	case control.TargetPickerRequestSourceList, control.TargetPickerRequestSourceUse, control.TargetPickerRequestSourceUseAll:
	default:
		return threadcatalogcontract.DesktopProjectSnapshot{}, false, nil
	}
	if record.LockedWorkspaceKey != "" ||
		s.surfaceBackend(surface) != agentproto.BackendCodex || s.catalog == nil {
		return threadcatalogcontract.DesktopProjectSnapshot{}, false, nil
	}
	snapshot, enabled, err := s.catalog.desktopProjects()
	if !enabled {
		return threadcatalogcontract.DesktopProjectSnapshot{}, false, nil
	}
	if err != nil {
		return snapshot, true, fmt.Errorf("读取桌面项目失败，请稍后重新打开选择卡：%w", err)
	}
	// CLI-only hosts have no desktop projects: their sessions are projectless.
	return snapshot, true, nil
}

func (s *Service) buildDesktopProjectPickerView(surface *state.SurfaceConsoleRecord, record *activeTargetPickerRecord, entries []workspaceSelectionEntry) (control.FeishuTargetPickerView, bool, error) {
	snapshot, enabled, err := s.desktopProjectPickerSnapshot(surface, record)
	if !enabled || err != nil {
		return control.FeishuTargetPickerView{}, enabled, err
	}
	index := newDesktopProjectIndex(snapshot)
	views := s.threadViewsVisibleInNormalList(surface, s.mergedThreadViews(surface))
	projectOptions := make([]control.FeishuTargetPickerWorkspaceOption, 0, len(snapshot.Projects)+1)
	for _, project := range snapshot.Projects {
		projectOptions = append(projectOptions, control.FeishuTargetPickerWorkspaceOption{
			Value: desktopProjectValuePrefix + project.ID, Label: project.Name,
		})
	}
	projectOptions = append(projectOptions, control.FeishuTargetPickerWorkspaceOption{
		Value: desktopProjectValuePrefix + desktopProjectlessID, Label: "未归属项目",
	})
	selectedProject := record.SelectedProjectID
	if selectedProject == "" {
		selectedProject = index.projectForWorkspace(s.surfaceCurrentWorkspaceKey(surface))
		for _, view := range views {
			if view.ThreadID == surface.SelectedThreadID {
				selectedProject = index.projectForThread(view)
				break
			}
		}
	}
	projectValue := desktopProjectValuePrefix + selectedProject
	projectPresent := targetPickerHasWorkspaceOption(projectOptions, projectValue)
	if !projectPresent {
		projectValue = ""
	}
	record.SelectedProjectID, record.ProjectGrouped = selectedProject, true

	// Each workspace is projected once using only this project's views. A moved
	// thread keeps its execution directory but appears under its explicit project.
	viewsByWorkspace := map[string][]*mergedThreadView{}
	for _, view := range views {
		if projectPresent && index.projectForThread(view) == selectedProject {
			key := mergedThreadWorkspaceClaimKey(view)
			viewsByWorkspace[key] = append(viewsByWorkspace[key], view)
		}
	}
	sessions := []control.FeishuTargetPickerSessionOption{}
	sessionWorkspaces := map[string]string{}
	for _, entry := range entries {
		if projectViews := viewsByWorkspace[entry.workspaceKey]; len(projectViews) != 0 {
			for _, option := range s.targetPickerSessionOptionsWithViews(surface, entry, record.Source, false, projectViews) {
				if option.Kind == control.FeishuTargetPickerSessionThread {
					sessions = append(sessions, option)
					sessionWorkspaces[option.Value] = entry.workspaceKey
				}
			}
		}
	}
	newWorkspace := ""
	if projectPresent && record.AllowNewThread {
		newWorkspace = s.desktopProjectNewThreadWorkspace(surface, selectedProject, index, entries)
		if newWorkspace != "" {
			sessions = append([]control.FeishuTargetPickerSessionOption{{
				Value: targetPickerNewThreadValue, Kind: control.FeishuTargetPickerSessionNewThread,
				Label: "新建会话", MetaText: "目录：" + newWorkspace,
			}}, sessions...)
			sessionWorkspaces[targetPickerNewThreadValue] = newWorkspace
		}
	}
	if projectPresent {
		for _, entry := range entries {
			if entry.worktreeBase && index.projectForWorkspace(entry.workspaceKey) == selectedProject {
				sessions = append(sessions, control.FeishuTargetPickerSessionOption{
					Value: targetPickerWorktreeCreateValue, Kind: control.FeishuTargetPickerSessionWorktree,
					Label: "从项目目录创建 Worktree",
				})
				sessionWorkspaces[targetPickerWorktreeCreateValue] = entry.workspaceKey
				break
			}
		}
	}
	selectedSession := strings.TrimSpace(record.SelectedSessionValue)
	if selectedSession == targetPickerAutoSession {
		selectedSession = ""
		if len(sessions) != 0 {
			selectedSession = sessions[0].Value
		}
	} else if !targetPickerHasSessionOption(sessions, selectedSession) {
		selectedSession = ""
	}
	workspace := sessionWorkspaces[selectedSession]
	record.SelectedWorkspaceKey, record.SelectedSessionValue = workspace, selectedSession
	if record.WorkspaceCursor < 0 {
		record.WorkspaceCursor = targetPickerWorkspaceOptionIndex(projectOptions, projectValue)
	}
	if record.SessionCursor < 0 {
		record.SessionCursor = targetPickerSessionOptionIndex(sessions, selectedSession)
	}
	record.WorkspaceCursor = normalizePickerDropdownCursor(record.WorkspaceCursor, len(projectOptions))
	record.SessionCursor = normalizePickerDropdownCursor(record.SessionCursor, len(sessions))
	projectLabel, _ := targetPickerSelectedWorkspaceSummary(projectOptions, projectValue)
	sessionLabel, sessionMeta := targetPickerSelectedSessionSummary(sessions, selectedSession)
	confirmLabel := "切换"
	if selectedSession == targetPickerNewThreadValue {
		confirmLabel = "新建会话"
	} else if selectedSession == targetPickerWorktreeCreateValue {
		confirmLabel = "继续创建"
	}
	messages := append([]control.FeishuTargetPickerMessage(nil), record.Messages...)
	if !projectPresent {
		messages = append(messages, control.FeishuTargetPickerMessage{Level: control.FeishuTargetPickerMessageWarning, Text: "项目已删除或归属已变化，请重新选择项目。"})
	} else if len(sessions) == 0 {
		messages = append(messages, control.FeishuTargetPickerMessage{Level: control.FeishuTargetPickerMessageInfo, Text: "当前项目没有可接管会话或可用目录，请选择其他项目，或从目录新建。"})
	}
	return control.NormalizeFeishuTargetPickerView(control.FeishuTargetPickerView{
		PickerID: record.PickerID, Title: "切换项目与会话", Source: record.Source,
		CatalogFamilyID: record.CatalogFamilyID, CatalogVariantID: record.CatalogVariantID, CatalogBackend: record.CatalogBackend,
		Stage: control.FeishuTargetPickerStageEditing, Page: control.FeishuTargetPickerPageTarget,
		StageLabel: "切换", Question: "切到哪个项目 / 会话？", ProjectGrouped: true,
		ShowWorkspaceSelect: true, ShowSessionSelect: true, AllowNewThread: record.AllowNewThread,
		WorkspacePlaceholder: "选择项目", SessionPlaceholder: "选择会话",
		WorkspaceCursor: record.WorkspaceCursor, SessionCursor: record.SessionCursor,
		WorkspaceSelectionValue: projectValue, SelectedWorkspaceKey: workspace, SelectedSessionValue: selectedSession,
		SelectedWorkspaceLabel: projectLabel, SelectedSessionLabel: sessionLabel, SelectedSessionMeta: sessionMeta,
		WorkspaceOptions: projectOptions, SessionOptions: sessions, ConfirmLabel: confirmLabel,
		CanConfirm: workspace != "" && selectedSession != "", Messages: messages,
		CanGoBack: len(record.BackValue) != 0, BackLabel: "返回上一层", BackValue: cloneTargetPickerActionPayload(record.BackValue),
	}), true, nil
}

func (s *Service) desktopProjectNewThreadWorkspace(surface *state.SurfaceConsoleRecord, projectID string, index *desktopProjectIndex, entries []workspaceSelectionEntry) string {
	candidates := index.roots[projectID]
	if projectID == desktopProjectlessID {
		if current := s.surfaceCurrentWorkspaceKey(surface); current != "" {
			candidates = append(candidates, current)
		}
		for _, entry := range entries {
			if index.projectForWorkspace(entry.workspaceKey) == desktopProjectlessID {
				candidates = append(candidates, entry.workspaceKey)
			}
		}
	}
	for _, path := range candidates {
		// Creating through the CLI has no explicit desktop assignment. Do not
		// promise a project whose shared root would infer into another project.
		if index.projectForWorkspace(path) != projectID {
			continue
		}
		if s.workspaceBusyOwnerForSurface(surface, path) != nil {
			continue
		}
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return path
		}
	}
	return ""
}
