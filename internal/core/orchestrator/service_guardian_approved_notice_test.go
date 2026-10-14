package orchestrator

import (
	"encoding/json"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/adapter/codex"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestGuardianReviewWireDisplayAndState(t *testing.T) {
	tests := []struct {
		name, message string
		visible       bool
	}{
		{"captured approved read", "Automatic approval review approved (risk: low, authorization: high): 用户明确授权通过该SSH主机继续实机检查。", false},
		{"captured approved diagnostic", "Automatic approval review approved (risk: medium, authorization: high): 已授权的限量 RF 诊断，不写 Flash。", false},
		{"approved high risk", "Automatic approval review approved (risk: high, authorization: medium): Approved action.", false},
		{"unknown critical risk", "Automatic approval review approved (risk: critical, authorization: high): Unknown format.", true},
		{"denied", "Automatic approval review denied (risk: high, authorization: low): Missing authorization.", true},
		{"generic warning", "Risky command blocked.", true},
		{"approved word in explanation", "Command blocked. A previous approved action is unrelated.", true},
		{"quoted approval", "Warning: Automatic approval review approved (risk: low, authorization: high): quoted text", true},
		{"future decision", "Automatic approval review needs_confirmation (risk: medium, authorization: high): Ask user.", true},
		{"future risk format", "Automatic approval review approved (risk: future, authorization: high): Unknown format.", true},
		{"future authorization", "Automatic approval review approved (risk: low, authorization: future): Unknown format.", true},
		{"truncated", "Automatic approval review approved (risk: medium, authorization: high", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc := prepareRemotePlanTurnForTest(t)
			surface := svc.root.Surfaces["surface-1"]
			activeItem := surface.ActiveQueueItemID
			raw, err := json.Marshal(map[string]any{"method": "guardianWarning", "params": map[string]any{"threadId": "thread-1", "message": test.message}})
			if err != nil {
				t.Fatal(err)
			}
			translated, err := codex.NewTranslator("inst-1").ObserveServer(raw)
			if err != nil || len(translated.Events) != 1 {
				t.Fatalf("notice translation: %v %#v", err, translated)
			}
			events := svc.ApplyAgentEvent("inst-1", translated.Events[0])
			if test.visible {
				if len(events) != 1 || events[0].Notice == nil || events[0].Notice.Title != "Codex 安全提醒" || events[0].Notice.Text != test.message {
					t.Fatalf("warning lost or changed: %#v", events)
				}
			} else if len(events) != 0 {
				t.Fatalf("approved review leaked a UI event: %#v", events)
			}
			inst := svc.root.Instances["inst-1"]
			for scope, notices := range map[string][]agentproto.ProtocolNotice{"instance": inst.ProtocolNotices, "thread": inst.Threads["thread-1"].ProtocolNotices} {
				if len(notices) != 1 || notices[0].Summary != test.message {
					t.Fatalf("%s lost diagnostic record: %#v", scope, notices)
				}
			}
			if surface.ActiveQueueItemID != activeItem || len(surface.PendingRequests) != 0 || surface.ActiveRequestCapture != nil {
				t.Fatal("passive review changed task or request-gate state")
			}
		})
	}
}

func TestGuardianApprovedReviewDoesNotCutSharedProgress(t *testing.T) {
	svc := prepareRemotePlanTurnForTest(t)
	surface := svc.root.Surfaces["surface-1"]
	surface.Verbosity = state.SurfaceVerbosityVerbose
	started := svc.ApplyAgentEvent("inst-1", agentproto.Event{Kind: agentproto.EventItemStarted, ThreadID: "thread-1", TurnID: "turn-1", ItemID: "cmd-1", ItemKind: "command_execution", Metadata: map[string]any{"command": "read-only diagnostic"}})
	if len(started) != 1 || started[0].ExecCommandProgress == nil {
		t.Fatalf("missing shared progress: %#v", started)
	}
	svc.RecordExecCommandProgressSegment("surface-1", "thread-1", "turn-1", "cmd-1", "om-progress-1")
	progress := surface.ActiveExecProgress
	events := svc.ApplyAgentEvent("inst-1", agentproto.Event{Kind: agentproto.EventProtocolNotice, ThreadID: "thread-1", TurnID: "turn-1", ProtocolNotice: &agentproto.ProtocolNotice{Method: "guardianWarning", ThreadID: "thread-1", TurnID: "turn-1", Summary: "Automatic approval review approved (risk: medium, authorization: high): 用户已授权。"}})
	if len(events) != 0 || surface.ActiveExecProgress != progress || progress == nil {
		t.Fatal("approved review produced a card or sealed ongoing progress")
	}
}
