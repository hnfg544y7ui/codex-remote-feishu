package orchestrator

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestScheduleDaemonCommandPreservesOriginalActor(t *testing.T) {
	s := &Service{}
	surface := &state.SurfaceConsoleRecord{GatewayID: "g", SurfaceSessionID: "s", ActorUserID: "later-group-actor"}
	events, ok := s.boundDaemonCommandEvents(surface, control.Action{Kind: control.ActionScheduleCommand,
		CommandID: control.FeishuCommandSchedule, ActorUserID: "original-actor", Text: "/schedule list", MessageID: "m"})
	if !ok || len(events) != 1 || events[0].DaemonCommand == nil {
		t.Fatalf("events = %#v", events)
	}
	command := events[0].DaemonCommand
	if command.ActorUserID != "original-actor" || command.GatewayID != "g" || command.SurfaceSessionID != "s" {
		t.Fatalf("immutable command owner was lost: %#v", command)
	}
}
