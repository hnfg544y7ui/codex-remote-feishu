package codex

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func settingsString(value string) *string { return &value }

func TestThreadSettingsSetCorrelatesAckAndRejectionWithoutInventingSettings(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, reject := range []bool{false, true} {
			t.Run(fmt.Sprintf("shared=%t/reject=%t", shared, reject), func(t *testing.T) {
				tr := NewTranslator("instance")
				tr.currentThreadID = "other-thread"
				if shared {
					tr.SetSharedBackend(true)
					sharedResume(t, tr, "thread-1")
				}
				frames, err := tr.TranslateCommand(agentproto.Command{Kind: agentproto.CommandThreadSettingsSet,
					CommandID: "command-1", Target: agentproto.Target{ThreadID: "thread-1"},
					ThreadSettingsSet: &agentproto.ThreadSettingsSetCommand{Model: settingsString("gpt-5.5-mini"), ReasoningEffort: settingsString("high")}})
				if err != nil || len(frames) != 1 {
					t.Fatalf("translate: %v", err)
				}
				var frame map[string]any
				if err := json.Unmarshal(frames[0], &frame); err != nil {
					t.Fatal(err)
				}
				params := frame["params"].(map[string]any)
				if frame["method"] != "thread/settings/update" || len(params) != 3 || params["threadId"] != "thread-1" || params["model"] != "gpt-5.5-mini" || params["effort"] != "high" {
					t.Fatalf("request: %#v", frame)
				}
				response := map[string]any{"id": frame["id"], "result": map[string]any{}}
				if reject {
					delete(response, "result")
					response["error"] = map[string]any{"code": -32601, "message": "Method not found"}
				}
				raw, _ := json.Marshal(response)
				result, err := tr.ObserveServer(raw)
				if err != nil || !result.Suppress || len(result.Events) != 1 {
					t.Fatalf("result: %v %#v", err, result)
				}
				event := result.Events[0]
				if event.Kind != agentproto.EventThreadSettingsSetResult || event.CommandID != "command-1" || event.ThreadID != "thread-1" || (event.ErrorMessage != "") != reject || event.ThreadSettings != nil {
					t.Fatalf("correlation: %#v", event)
				}
				if len(tr.observedThreads) != 0 {
					t.Fatal("ACK fabricated effective settings")
				}
				duplicate, err := tr.ObserveServer(raw)
				if err != nil || len(duplicate.Events) != 0 {
					t.Fatal("duplicate ACK emitted another result")
				}
			})
		}
	}
}

func TestThreadSettingsSetPartialUpdates(t *testing.T) {
	for _, item := range []struct {
		command agentproto.ThreadSettingsSetCommand
		field   string
		value   any
	}{
		{agentproto.ThreadSettingsSetCommand{Model: settingsString("gpt-5.5")}, "model", "gpt-5.5"},
		{agentproto.ThreadSettingsSetCommand{ReasoningEffort: settingsString("medium")}, "effort", "medium"},
	} {
		tr := NewTranslator("i")
		frames, err := tr.TranslateCommand(agentproto.Command{Kind: agentproto.CommandThreadSettingsSet, CommandID: "c", Target: agentproto.Target{ThreadID: "t"}, ThreadSettingsSet: &item.command})
		if err != nil {
			t.Fatal(err)
		}
		var request map[string]any
		_ = json.Unmarshal(frames[0], &request)
		params := request["params"].(map[string]any)
		if len(params) != 2 || params[item.field] != item.value {
			t.Fatalf("partial update changed unrelated fields: %#v", params)
		}
	}
}

func TestThreadSettingsSetRejectsEmptyEffortWithoutSendingPartialMutation(t *testing.T) {
	for _, effort := range []string{"", " "} {
		for _, model := range []*string{nil, settingsString("gpt-5.5")} {
			tr := NewTranslator("i")
			frames, err := tr.TranslateCommand(agentproto.Command{Kind: agentproto.CommandThreadSettingsSet,
				CommandID: "c", Target: agentproto.Target{ThreadID: "t"},
				ThreadSettingsSet: &agentproto.ThreadSettingsSetCommand{Model: model, ReasoningEffort: settingsString(effort)}})
			if err == nil || len(frames) != 0 || len(tr.pendingThreadSettingsSet) != 0 {
				t.Fatalf("empty effort accepted or partially dispatched: %v %s", err, frames)
			}
		}
	}
}

func TestThreadSettingsNotificationsReadActualShapeAndClearNullReasoning(t *testing.T) {
	tr := NewTranslator("i")
	for _, frame := range []string{
		`{"method":"thread/settings/updated","params":{"threadId":"t","threadSettings":{"model":"gpt-5.5-mini","effort":"high"}}}`,
		`{"method":"thread/settings/updated","params":{"threadId":"t","threadSettings":{"effort":null}}}`,
	} {
		result, err := tr.ObserveServer([]byte(frame))
		if err != nil || len(result.Events) != 1 {
			t.Fatalf("notification: %v %#v", err, result)
		}
		update := result.Events[0].ThreadSettings
		if update == nil || !update.ReasoningEffortSet {
			t.Fatalf("effort presence lost: %#v", update)
		}
	}
	if tr.observedThreads["t"].ReasoningEffort != "" || tr.observedThreads["t"].Model != "gpt-5.5-mini" {
		t.Fatal("null effort retained stale reasoning or cleared the model")
	}
	result, err := tr.ObserveServer([]byte(`{"method":"thread/settings/updated","params":{"threadId":"u","settings":{"model":"gpt-5.5","reasoningEffort":"medium"}}}`))
	if err != nil || len(result.Events) != 1 || result.Events[0].ThreadSettings.ReasoningEffort != "medium" {
		t.Fatal("legacy notification broken")
	}
	if tr.observedThreads["t"].ReasoningEffort != "" {
		t.Fatal("other thread notification changed this thread")
	}
}

func TestThreadSettingsSetRejectsMissingFieldsAndMalformedAck(t *testing.T) {
	tr := NewTranslator("i")
	for _, update := range []*agentproto.ThreadSettingsSetCommand{nil, {}, {Model: settingsString(" ")}} {
		if _, err := tr.TranslateCommand(agentproto.Command{Kind: agentproto.CommandThreadSettingsSet, CommandID: "c", Target: agentproto.Target{ThreadID: "t"}, ThreadSettingsSet: update}); err == nil {
			t.Fatal("accepted malformed settings update")
		}
	}
	frames, _ := tr.TranslateCommand(agentproto.Command{Kind: agentproto.CommandThreadSettingsSet, CommandID: "c", Target: agentproto.Target{ThreadID: "t"}, ThreadSettingsSet: &agentproto.ThreadSettingsSetCommand{Model: settingsString("m")}})
	var frame map[string]any
	_ = json.Unmarshal(frames[0], &frame)
	raw, _ := json.Marshal(map[string]any{"id": frame["id"], "result": nil})
	result, err := tr.ObserveServer(raw)
	if err != nil || len(result.Events) != 1 || result.Events[0].ErrorMessage == "" {
		t.Fatal("malformed ACK reported success")
	}
}

func TestThreadSettingsSetOutOfOrderResultsKeepThreadAndCommandCorrelation(t *testing.T) {
	tr := NewTranslator("i")
	ids := make([]any, 0, 2)
	for _, target := range []string{"a", "b"} {
		frames, err := tr.TranslateCommand(agentproto.Command{Kind: agentproto.CommandThreadSettingsSet,
			CommandID: "command-" + target, Target: agentproto.Target{ThreadID: target},
			ThreadSettingsSet: &agentproto.ThreadSettingsSetCommand{Model: settingsString("model-" + target)}})
		if err != nil {
			t.Fatal(err)
		}
		var frame map[string]any
		_ = json.Unmarshal(frames[0], &frame)
		ids = append(ids, frame["id"])
	}
	for index := 1; index >= 0; index-- {
		target := []string{"a", "b"}[index]
		raw, _ := json.Marshal(map[string]any{"id": ids[index], "result": map[string]any{}})
		result, err := tr.ObserveServer(raw)
		if err != nil || len(result.Events) != 1 || result.Events[0].ThreadID != target || result.Events[0].CommandID != "command-"+target {
			t.Fatalf("cross-thread response: %v %#v", err, result)
		}
	}
}
