package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

type asyncQuestionTestGateway struct {
	recordingGateway
	next int
}

func (g *asyncQuestionTestGateway) Apply(ctx context.Context, ops []feishu.Operation) error {
	for i := range ops {
		if ops[i].Kind == feishu.OperationSendCard && ops[i].MessageID == "" {
			g.next++
			ops[i].MessageID = fmt.Sprintf("sent-card-%d", g.next)
		}
	}
	return g.recordingGateway.Apply(ctx, ops)
}

func TestAsyncQuestionFastProbeAndAckKeepUnrelatedPromptBinding(t *testing.T) {
	gateway := &asyncQuestionTestGateway{}
	app := New(":0", ":0", gateway, agentproto.ServerIdentity{})
	app.daemonLifecycleID = "async-test-lifecycle"
	var sent []agentproto.Command
	app.sendAgentCommand = func(instance string, cmd agentproto.Command) error {
		if instance != "inst-1" {
			t.Fatal("answer retargeted instance", instance)
		}
		sent = append(sent, cmd)
		if cmd.Kind == agentproto.CommandThreadHistoryRead {
			app.onEvents(context.Background(), instance, []agentproto.Event{{Kind: agentproto.EventThreadHistoryRead, CommandID: cmd.CommandID, ThreadID: "thread-1",
				ThreadHistory: &agentproto.ThreadHistoryRecord{Thread: agentproto.ThreadSnapshotRecord{ThreadID: "thread-1", RuntimeStatus: &agentproto.ThreadRuntimeStatus{Type: agentproto.ThreadRuntimeStatusTypeActive}}, Turns: []agentproto.ThreadHistoryTurnRecord{{TurnID: "native-turn", Status: "inProgress"}}}}})
		} else if cmd.Kind == agentproto.CommandTurnSteer {
			app.onCommandAck(context.Background(), instance, agentproto.CommandAck{CommandID: cmd.CommandID, Accepted: true})
		}
		return nil
	}
	workspace := evalSymlinkForTest(t, t.TempDir())
	app.service.UpsertInstance(&state.InstanceRecord{InstanceID: "inst-1", Online: true, WorkspaceRoot: workspace, WorkspaceKey: workspace, ObservedFocusedThreadID: "thread-1", Threads: map[string]*state.ThreadRecord{"thread-1": {ThreadID: "thread-1", Loaded: true, CWD: workspace, WorkspaceKey: workspace}}})
	for _, a := range []control.Action{{Kind: control.ActionAttachInstance, InstanceID: "inst-1"}, {Kind: control.ActionUseThread, ThreadID: "thread-1"}, {Kind: control.ActionTextMessage, MessageID: "original", Text: "original"}} {
		a.SurfaceSessionID, a.ChatID, a.ActorUserID = "surface-1", "chat-1", "user-1"
		app.HandleAction(context.Background(), a)
	}
	before := app.service.PendingRemoteTurns()
	if len(before) != 1 {
		t.Fatal("missing original unconfirmed prompt")
	}
	app.onEvents(context.Background(), "inst-1", []agentproto.Event{{Kind: agentproto.EventItemCompleted, ThreadID: "thread-1", TurnID: "native-turn", ItemID: "q", ItemKind: "agent_message",
		Metadata: map[string]any{"text": "检查哪个项目？", "messageDelivery": "async", "hasAsyncQuestions": true}, AsyncQuestions: []agentproto.AsyncQuestion{{ID: "native-question", Title: "检查哪个项目？"}}}})
	var source feishu.Operation
	for _, op := range gateway.operations {
		if op.CardTitle == "需要你回答" {
			source = op
		}
	}
	if source.MessageID == "" || source.AttentionUserID != "user-1" {
		t.Fatal("first question delivery did not capture message or real mention", source)
	}
	data, _ := json.Marshal(source.CardElements)
	var decoded any
	_ = json.Unmarshal(data, &decoded)
	var id string
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if x, ok := v["async_question_id"].(string); ok {
				id = x
			}
			for _, x := range v {
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(decoded)
	if id == "" {
		t.Fatal("real projected question lacks callback identity")
	}
	action := control.Action{Kind: control.ActionAsyncQuestionSubmit, SurfaceSessionID: "surface-1", ChatID: "chat-1", ActorUserID: "user-1", MessageID: source.MessageID, AsyncQuestionID: id, AsyncQuestionRevision: 1, AsyncQuestionAnswers: map[string]string{"answer_0": "网络"}, Inbound: &control.ActionInboundMeta{CardCallback: true, CardDaemonLifecycleID: app.daemonLifecycleID}}
	if result := app.HandleGatewayAction(context.Background(), action); result != nil {
		t.Fatal("question relied on a late synchronous replacement")
	}
	queries, answers, prompts := 0, 0, 0
	for _, cmd := range sent {
		switch cmd.Kind {
		case agentproto.CommandThreadHistoryRead:
			queries++
		case agentproto.CommandPromptSend:
			prompts++
		case agentproto.CommandTurnSteer:
			answers++
			if cmd.Target.TurnID != "native-turn" || !strings.Contains(cmd.Prompt.Inputs[0].Text, "native-question") {
				t.Fatal("incorrect answer target or text")
			}
		}
	}
	after := app.service.PendingRemoteTurns()
	if queries != 1 || answers != 1 || prompts != 1 || len(after) != 1 || before[0].CommandID != after[0].CommandID || len(app.pendingThreadHistoryReads) != 0 {
		t.Fatal("answer pipeline duplicated a prompt, rebound ordinary work, or leaked history flow", sent, before, after)
	}
	last := gateway.operations[len(gateway.operations)-1]
	if last.Kind != feishu.OperationUpdateCard || last.MessageID != source.MessageID || last.AttentionUserID != "" {
		t.Fatal("answer did not finish on its original card without another mention", last)
	}
	beforeDuplicate := len(sent)
	app.HandleGatewayAction(context.Background(), action)
	if len(sent) != beforeDuplicate {
		t.Fatal("duplicate stale submit resent input")
	}
}
