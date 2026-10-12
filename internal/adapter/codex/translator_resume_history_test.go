package codex

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestResumeHistoryPolicyPreservesPromptAndCompactContinuation(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, kind := range []agentproto.CommandKind{agentproto.CommandPromptSend, agentproto.CommandThreadCompactStart} {
			t.Run(fmt.Sprintf("shared=%t/%s", shared, kind), func(t *testing.T) {
				tr := NewTranslator("resume-history")
				tr.SetSharedBackend(shared)
				frames, err := tr.TranslateCommand(agentproto.Command{
					Kind: kind, CommandID: "resume-command", Origin: agentproto.Origin{Surface: "surface"},
					Target: agentproto.Target{ThreadID: "long-thread", CWD: "/tmp/project"},
					Prompt: agentproto.Prompt{Inputs: []agentproto.Input{{Type: agentproto.InputText, Text: "continue objective"}}},
				})
				if err != nil || len(frames) != 1 {
					t.Fatalf("resume: %v (%d frames)", err, len(frames))
				}
				request := historyRequest(t, frames, "thread/resume")
				params := request["params"].(map[string]any)
				if shared && params["excludeTurns"] != true {
					t.Fatal("shared resume hydrates full history")
				}
				if !shared && params["excludeTurns"] != nil {
					t.Fatal("private stdio hydration changed")
				}
				response, _ := json.Marshal(map[string]any{"id": request["id"], "result": map[string]any{
					"thread": map[string]any{"id": "long-thread", "cwd": "/tmp/project", "turns": []any{}},
				}})
				result, err := tr.ObserveServer(response)
				if err != nil || len(result.OutboundToCodex) != 1 {
					t.Fatalf("metadata-only resume lost continuation: %v %#v", err, result)
				}
				method := "turn/start"
				if kind == agentproto.CommandThreadCompactStart {
					method = "thread/compact/start"
				}
				followup := historyRequest(t, result.OutboundToCodex, method)
				if followup["params"].(map[string]any)["threadId"] != "long-thread" {
					t.Fatal("continuation changed target")
				}
				if kind == agentproto.CommandPromptSend {
					input := followup["params"].(map[string]any)["input"].([]any)
					if len(input) != 1 || input[0].(map[string]any)["text"] != "continue objective" {
						t.Fatalf("continuation lost original input: %#v", input)
					}
				}
			})
		}
	}
}

func TestRestartRestoreExcludesTurnsOnlyForSharedBackend(t *testing.T) {
	for _, shared := range []bool{false, true} {
		tr := NewTranslator("restore-history")
		tr.SetSharedBackend(shared)
		if _, err := tr.ObserveClient([]byte(`{"method":"thread/resume","params":{"threadId":"long-thread","cwd":"/tmp/project"}}`)); err != nil {
			t.Fatal(err)
		}
		frame, _, ok, err := tr.BuildChildRestartRestoreFrame("restore-command")
		if err != nil || !ok {
			t.Fatalf("restore: %v", err)
		}
		request := historyRequest(t, [][]byte{frame}, "thread/resume")
		params := request["params"].(map[string]any)
		if params["threadId"] != "long-thread" || (shared && params["excludeTurns"] != true) || (!shared && params["excludeTurns"] != nil) {
			t.Fatalf("incorrect restore policy (shared=%t): %#v", shared, params)
		}
	}
}
