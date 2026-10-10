package codex

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestRemoteThreadNameSetResult(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			tr := NewTranslator("instance-1")
			tr.currentThreadID = "other-thread"
			out, err := tr.TranslateCommand(agentproto.Command{
				Kind: agentproto.CommandThreadNameSet, CommandID: "command-1",
				Target: agentproto.Target{ThreadID: "thread-1"}, Name: "  摄像头  持续录制  ",
			})
			if err != nil || len(out) != 1 {
				t.Fatalf("translate: %v, %#v", err, out)
			}
			var request map[string]any
			if err := json.Unmarshal(out[0], &request); err != nil {
				t.Fatal(err)
			}
			params := request["params"].(map[string]any)
			if request["method"] != "thread/name/set" || params["threadId"] != "thread-1" || params["name"] != "摄像头  持续录制" {
				t.Fatalf("wrong request: %#v", request)
			}
			response := map[string]any{"id": request["id"], "result": map[string]any{}}
			if failure {
				delete(response, "result")
				response["error"] = map[string]any{"code": -32601, "message": "Method not found"}
			}
			raw, _ := json.Marshal(response)
			result, err := tr.ObserveServer(raw)
			if err != nil || !result.Suppress || len(result.Events) != 1 {
				t.Fatalf("response: %v, %#v", err, result)
			}
			event := result.Events[0]
			if event.Kind != agentproto.EventThreadNameCommandResult || event.CommandID != "command-1" || event.ThreadID != "thread-1" {
				t.Fatalf("incorrect correlation: %#v", event)
			}
			if failure && (event.ErrorMessage != "Method not found" || event.Name != "") {
				t.Fatalf("failure: %#v", event)
			}
			if !failure && (event.Name != "摄像头  持续录制" || event.ErrorMessage != "") {
				t.Fatalf("success: %#v", event)
			}
			if tr.currentThreadID != "other-thread" || len(result.OutboundToCodex) != 0 {
				t.Fatal("rename changed active thread or started a turn")
			}
			duplicate, err := tr.ObserveServer(raw)
			if err != nil || len(duplicate.Events) != 0 {
				t.Fatalf("duplicate response: %v, %#v", err, duplicate)
			}
		})
	}
}

func TestRemoteThreadNameSetRequiresExplicitTargetAndName(t *testing.T) {
	for _, command := range []agentproto.Command{
		{CommandID: "c", Name: "name"},
		{CommandID: "c", Target: agentproto.Target{ThreadID: "t"}, Name: "  "},
		{Target: agentproto.Target{ThreadID: "t"}, Name: "name"},
	} {
		command.Kind = agentproto.CommandThreadNameSet
		tr := NewTranslator("i")
		out, err := tr.TranslateCommand(command)
		if err == nil || len(out) != 0 || len(tr.pendingThreadNameSet) != 0 {
			t.Fatalf("accepted invalid command: %#v", command)
		}
	}
}

func TestThreadNameSetMalformedResponseDoesNotReportSuccess(t *testing.T) {
	tr := NewTranslator("i")
	out, err := tr.TranslateCommand(agentproto.Command{
		Kind: agentproto.CommandThreadNameSet, CommandID: "c",
		Target: agentproto.Target{ThreadID: "t"}, Name: "中文",
	})
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]any
	if err := json.Unmarshal(out[0], &request); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"id": request["id"]})
	result, err := tr.ObserveServer(raw)
	if err != nil || len(result.Events) != 1 || result.Events[0].ErrorMessage == "" || result.Events[0].Name != "" {
		t.Fatalf("malformed response reported success: %v, %#v", err, result)
	}
}

func TestLocalThreadNameSetObservationStillPassesResponseThrough(t *testing.T) {
	tr := NewTranslator("i")
	if _, err := tr.ObserveClient([]byte(`{"id":"local-name","method":"thread/name/set","params":{"threadId":"local-thread","name":"本地名称"}}`)); err != nil {
		t.Fatal(err)
	}
	result, err := tr.ObserveServer([]byte(`{"id":"local-name","result":{}}`))
	if err != nil || result.Suppress || len(result.Events) != 1 || result.Events[0].Kind != agentproto.EventThreadDiscovered || result.Events[0].Name != "本地名称" {
		t.Fatalf("local observation changed: %v, %#v", err, result)
	}
}

func TestThreadNameUpdatedNotificationUsesNativeThreadName(t *testing.T) {
	tr := NewTranslator("i")
	result, err := tr.ObserveServer([]byte(`{"method":"thread/name/updated","params":{"threadId":"t","threadName":"正式中文名称"}}`))
	if err != nil || len(result.Events) != 1 || result.Events[0].ThreadID != "t" || result.Events[0].Name != "正式中文名称" {
		t.Fatalf("lost native name notification: %v, %#v", err, result)
	}
}
