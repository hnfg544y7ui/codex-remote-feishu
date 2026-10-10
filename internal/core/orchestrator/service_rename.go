package orchestrator

import (
	"strings"
	"time"
	"unicode"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/frontstagecontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/xutil"
)

const renameCommandTimeout = 30 * time.Second

type renameCommandBinding struct {
	SurfaceID  string
	InstanceID string
	ThreadID   string
	Name       string
	MessageID  string
	TrackingID string
	ExpiresAt  time.Time
}

// RenameCommandInstanceID returns the immutable instance selected at dispatch.
func (s *Service) RenameCommandInstanceID(commandID string) string {
	return s.renameCommands[commandID].InstanceID
}

func (s *Service) handleRenameCommandDispatchFailure(surfaceID, commandID string, err error) []eventcontract.Event {
	binding, ok := s.renameCommands[commandID]
	if !ok || binding.SurfaceID != surfaceID {
		return nil
	}
	text := "重命名请求未成功发送到 Codex。"
	if err != nil {
		text += " " + err.Error()
	}
	return s.failRenameCommand(commandID, "rename_dispatch_failed", text)
}

func (s *Service) handleRenameCommandRejected(instanceID string, ack agentproto.CommandAck) []eventcontract.Event {
	binding, ok := s.renameCommands[ack.CommandID]
	if !ok || binding.InstanceID != instanceID {
		return nil
	}
	problem := commandAckProblem(binding.SurfaceID, ack)
	return s.failRenameCommand(ack.CommandID, "rename_rejected", "Codex 拒绝重命名请求。 "+problem.Message)
}

func (s *Service) handleRenameCommand(surface *state.SurfaceConsoleRecord, action control.Action) []eventcontract.Event {
	if surface == nil {
		return nil
	}
	binding := renameCommandBinding{SurfaceID: surface.SurfaceSessionID, ThreadID: strings.TrimSpace(surface.SelectedThreadID)}
	if commandCardOwnsInlineResult(action) {
		binding.MessageID = strings.TrimSpace(action.MessageID)
	}
	reject := func(text string) []eventcontract.Event {
		return []eventcontract.Event{s.renameCommandPage(binding, "error", text, true)}
	}
	if !s.surfaceIsHeadless(surface) || s.surfaceBackend(surface) != agentproto.BackendCodex {
		return reject("重命名会话仅支持 Codex headless 模式。请先 /mode codex 并选择会话。")
	}
	inst := s.root.Instances[surface.AttachedInstanceID]
	if inst == nil || !inst.Online || inst.Backend != "" && inst.Backend != agentproto.BackendCodex {
		return reject("当前 Codex 实例不可用，请先 /list 选择在线工作区和会话。")
	}
	if binding.ThreadID == "" || !s.surfaceOwnsThread(surface, binding.ThreadID) || !ordinaryThreadVisible(inst.Threads[binding.ThreadID]) {
		return reject("当前没有精确选中且可操作的会话。请先 /list 或 /use 选择一个 Codex 会话。")
	}
	binding.InstanceID = inst.InstanceID
	formSubmission := commandCardOwnsInlineResult(action) && strings.TrimSpace(action.FieldName) != ""
	if formSubmission && (action.FieldName != "command_args" || action.InstanceID != inst.InstanceID || action.ThreadID != binding.ThreadID) {
		return reject("当前选中的工作区或会话已变化，这张重命名表单已失效。请从当前菜单重新打开。")
	}
	text := strings.TrimSpace(action.Text)
	separator := strings.IndexFunc(text, unicode.IsSpace)
	if separator >= 0 {
		binding.Name = strings.TrimSpace(text[separator:])
	}
	if binding.Name == "" {
		if commandCardOwnsInlineResult(action) && action.LocalPageAction && !formSubmission {
			return []eventcontract.Event{s.renameCommandFormPage(surface, action, binding, "")}
		}
		if formSubmission {
			return []eventcontract.Event{s.renameCommandFormPage(surface, action, binding, "名称不能为空，原会话名称未修改。请输入新名称后再提交。")}
		}
		return reject("用法：/rename 新名称。名称不能为空，原会话名称未修改。")
	}
	for _, pending := range s.renameCommands {
		if pending.InstanceID == inst.InstanceID && pending.ThreadID == binding.ThreadID {
			return reject("该会话已有重命名请求正在等待 Codex 确认，请稍后再试。")
		}
	}
	commandID := "relay-rename-" + strings.TrimPrefix(s.nextAgentCommandID(), "relay-goal-")
	binding.InstanceID = inst.InstanceID
	binding.TrackingID = commandID
	binding.ExpiresAt = s.now().Add(renameCommandTimeout)
	if s.renameCommands == nil {
		s.renameCommands = map[string]renameCommandBinding{}
	}
	s.renameCommands[commandID] = binding
	return []eventcontract.Event{
		s.renameCommandPage(binding, "info", "正在等待 Codex 保存会话名称。", false),
		{
			Kind:             eventcontract.KindAgentCommand,
			SurfaceSessionID: surface.SurfaceSessionID,
			Command: &agentproto.Command{
				Kind:      agentproto.CommandThreadNameSet,
				CommandID: commandID,
				Name:      binding.Name,
				Target:    agentproto.Target{ThreadID: binding.ThreadID},
				Origin: agentproto.Origin{
					Surface:   surface.SurfaceSessionID,
					UserID:    surface.ActorUserID,
					ChatID:    surface.ChatID,
					MessageID: action.MessageID,
				},
			},
		},
	}
}

func (s *Service) renameCommandFormPage(surface *state.SurfaceConsoleRecord, action control.Action, binding renameCommandBinding, problem string) eventcontract.Event {
	thread := s.root.Instances[binding.InstanceID].Threads[binding.ThreadID]
	defaultName := thread.Name
	if len([]rune(defaultName)) > 300 {
		defaultName = ""
		if problem == "" {
			problem = "当前名称较长，请在输入框填写新名称。"
		}
	}
	form := control.FeishuCommandFormWithDefault(control.FeishuCommandRename, defaultName)
	form.CatalogFamilyID, form.CatalogVariantID, form.CatalogBackend = s.catalogProvenanceForAction(surface, action)
	// A trusted local menu launcher intentionally has no command provenance.
	// Resolve it for this validated form; submitted callbacks remain subject to
	// the ordinary strict provenance and exact-target checks before reaching here.
	if resolved, ok := control.ResolveFeishuActionCatalog(s.buildCatalogContext(surface), action); ok {
		form.CatalogFamilyID = resolved.Action.CatalogFamilyID
		form.CatalogVariantID = resolved.Action.CatalogVariantID
		form.CatalogBackend = resolved.Action.CatalogBackend
	}
	form.SubmitValue = frontstagecontract.ActionPayloadWithCatalog(
		frontstagecontract.ActionPayloadPageSubmit(string(control.ActionRenameCommand), "", "command_args"),
		form.CatalogFamilyID, form.CatalogVariantID, string(form.CatalogBackend),
	)
	form.SubmitValue[frontstagecontract.CardActionPayloadKeyInstanceID] = binding.InstanceID
	form.SubmitValue[frontstagecontract.CardActionPayloadKeyThreadID] = binding.ThreadID
	// Form pages are returned through the fresh card's synchronous callback.
	// Leaving MessageID empty projects a SendCard replacement and avoids a REST
	// update racing the callback's acknowledgement of the original menu card.
	// Subsequent rename requests still bind the callback's original MessageID.
	view := control.FeishuPageView{
		PageID: control.FeishuCommandRename, CommandID: control.FeishuCommandRename, Title: "重命名会话",
		Patchable: true, Interactive: true, ThemeKey: "primary",
		TemporarySessionLabel: s.ResolveTemporarySessionLabel(binding.SurfaceID, binding.InstanceID, binding.ThreadID, ""),
		BodySections: []control.FeishuCardTextSection{
			{Label: "目标会话", Lines: []string{renameCardText(binding.ThreadID, 100)}},
			{Label: "当前名称", Lines: []string{renameCardText(thread.Name, 300)}},
		},
		Sections: []control.CommandCatalogSection{{Title: "新名称", Entries: []control.CommandCatalogEntry{{Form: form}}}},
	}
	if problem != "" {
		view.NoticeSections = []control.FeishuCardTextSection{{Lines: []string{problem}}}
	}
	return s.pageEvent(surface, control.NormalizeFeishuPageView(view))
}

func (s *Service) renameCommandPage(binding renameCommandBinding, status, text string, sealed bool) eventcontract.Event {
	surface := s.root.Surfaces[binding.SurfaceID]
	view := control.FeishuPageView{
		PageID:                control.FeishuCommandRename,
		CommandID:             control.FeishuCommandRename,
		Title:                 "重命名会话",
		TemporarySessionLabel: s.ResolveTemporarySessionLabel(binding.SurfaceID, binding.InstanceID, binding.ThreadID, ""),
		MessageID:             binding.MessageID,
		Patchable:             true,
		Sealed:                sealed,
		BodySections: []control.FeishuCardTextSection{
			{Label: "目标会话", Lines: []string{renameCardText(binding.ThreadID, 100)}},
		},
		NoticeSections: []control.FeishuCardTextSection{{Lines: []string{renameCardText(text, 2000)}}},
		StatusKind:     status,
	}
	if binding.Name != "" {
		view.BodySections = append(view.BodySections, control.FeishuCardTextSection{Label: "新名称", Lines: []string{renameCardText(binding.Name, 300)}})
	}
	if binding.MessageID == "" {
		view.TrackingKey = binding.TrackingID
	}
	if !sealed {
		view.ThemeKey = "progress"
	}
	return s.pageEvent(surface, control.NormalizeFeishuPageView(view))
}

func renameCardText(text string, limit int) string {
	return xutil.TruncateRunes(text, limit, xutil.TruncateOptions{Ellipsis: "…", ReserveEllipsis: true})
}

func (s *Service) applyRenameCommandResult(instanceID string, event agentproto.Event) []eventcontract.Event {
	binding, ok := s.renameCommands[event.CommandID]
	if !ok || binding.InstanceID != instanceID || binding.ThreadID != event.ThreadID {
		return nil
	}
	if !s.now().Before(binding.ExpiresAt) {
		return s.failRenameCommand(event.CommandID, "rename_timeout", "等待 Codex 确认超时。名称可能已保存，请刷新会话列表核实后重试。")
	}
	if event.ErrorMessage != "" {
		return s.failRenameCommand(event.CommandID, "rename_failed", "Codex 未能保存会话名称。 "+event.ErrorMessage)
	}
	if event.Name != binding.Name {
		return s.failRenameCommand(event.CommandID, "rename_invalid_result", "Codex 返回的名称与请求不一致，无法确认重命名成功。")
	}
	inst := s.root.Instances[binding.InstanceID]
	if inst == nil || inst.Threads[binding.ThreadID] == nil {
		return s.failRenameCommand(event.CommandID, "rename_thread_lost", "目标会话已不在本机目录中，无法确认名称更新。")
	}
	delete(s.renameCommands, event.CommandID)
	inst.Threads[binding.ThreadID].Name = binding.Name
	if s.root.Surfaces[binding.SurfaceID] == nil {
		return nil
	}
	return []eventcontract.Event{s.renameCommandPage(binding, "success", "Codex 已保存会话名称。", true)}
}

func (s *Service) failRenameCommand(commandID, _ string, text string) []eventcontract.Event {
	binding, ok := s.renameCommands[commandID]
	if !ok {
		return nil
	}
	delete(s.renameCommands, commandID)
	if s.root.Surfaces[binding.SurfaceID] == nil {
		return nil
	}
	return []eventcontract.Event{s.renameCommandPage(binding, "error", text, true)}
}

func (s *Service) tickRenameCommands(now time.Time) []eventcontract.Event {
	var events []eventcontract.Event
	for commandID, binding := range s.renameCommands {
		if s.root.Surfaces[binding.SurfaceID] == nil {
			delete(s.renameCommands, commandID)
		} else if s.root.Instances[binding.InstanceID] == nil {
			events = append(events, s.failRenameCommand(commandID, "rename_instance_lost", "目标 Codex 实例已移除，无法确认名称保存。")...)
		} else if !now.Before(binding.ExpiresAt) {
			events = append(events, s.failRenameCommand(commandID, "rename_timeout", "等待 Codex 确认超时。名称可能已保存，请刷新会话列表核实后重试。")...)
		}
	}
	return events
}

func (s *Service) failRenameCommandsForInstance(instanceID string) []eventcontract.Event {
	var events []eventcontract.Event
	for commandID, binding := range s.renameCommands {
		if binding.InstanceID == instanceID {
			events = append(events, s.failRenameCommand(commandID, "rename_disconnected", "Codex 连接已中断，无法确认会话名称是否保存。请重新连接后刷新会话列表核实。")...)
		}
	}
	return events
}
