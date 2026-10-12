package gateway

import (
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
)

func parseNaturalGoalInboundAction(text string, base control.Action) (control.Action, bool) {
	if strings.TrimSpace(base.ActorUserID) == "" {
		return control.Action{}, false
	}
	action, ok := control.ParseFeishuNaturalGoalAction(text)
	if !ok {
		return control.Action{}, false
	}
	action.GatewayID = base.GatewayID
	action.SurfaceSessionID = base.SurfaceSessionID
	action.ChatID = base.ChatID
	action.ActorUserID = base.ActorUserID
	action.MessageID = base.MessageID
	action.TargetMessageID = base.TargetMessageID
	action.Inbound = cloneInboundMeta(base.Inbound)
	return action, true
}
