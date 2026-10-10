package orchestrator

import (
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func (s *Service) boundDaemonCommandEvents(surface *state.SurfaceConsoleRecord, action control.Action) ([]eventcontract.Event, bool) {
	binding, ok := control.ResolveFeishuCommandBindingFromAction(action)
	if !ok || binding.DirectDaemonCommand == "" {
		return nil, false
	}
	command := &control.DaemonCommand{
		Kind:             binding.DirectDaemonCommand,
		GatewayID:        surface.GatewayID,
		SurfaceSessionID: surface.SurfaceSessionID,
		SourceMessageID:  action.MessageID,
		ActorUserID:      action.ActorUserID,
		Text:             action.Text,
	}
	if binding.PropagateCardActionToDaemon || action.LocalPageAction {
		command.FromCardAction = action.IsCardAction()
	}
	return []eventcontract.Event{{
		Kind:             eventcontract.KindDaemonCommand,
		GatewayID:        surface.GatewayID,
		SurfaceSessionID: surface.SurfaceSessionID,
		SourceMessageID:  action.MessageID,
		DaemonCommand:    command,
	}}, true
}
