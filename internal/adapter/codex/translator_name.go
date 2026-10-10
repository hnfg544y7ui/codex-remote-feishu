package codex

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/jsonrpcutil"
)

func (t *Translator) translateThreadNameSet(command agentproto.Command) ([][]byte, error) {
	threadID, name := strings.TrimSpace(command.Target.ThreadID), strings.TrimSpace(command.Name)
	if threadID == "" || name == "" || strings.TrimSpace(command.CommandID) == "" {
		return nil, fmt.Errorf("thread.name.set requires command id, thread id and nonempty name")
	}
	requestID := t.NextRequest("thread-name-set")
	payload, err := json.Marshal(map[string]any{
		"id": requestID, "method": "thread/name/set",
		"params": map[string]any{"threadId": threadID, "name": name},
	})
	if err != nil {
		return nil, err
	}
	t.pendingThreadNameSet[requestID] = pendingThreadNameSet{
		CommandID: command.CommandID, ThreadID: threadID, Name: name,
	}
	return [][]byte{append(payload, '\n')}, nil
}

func threadNameSetResult(pending pendingThreadNameSet, message map[string]any) Result {
	remote := pending.CommandID != ""
	result := Result{Suppress: remote}
	_, hasError := message["error"]
	_, hasResult := message["result"]
	if hasError || !hasResult {
		if remote {
			errorMessage := jsonrpcutil.ExtractErrorMessage(message)
			if errorMessage == "" {
				errorMessage = "Codex returned an invalid thread/name/set response"
			}
			result.Events = []agentproto.Event{{
				Kind: agentproto.EventThreadNameCommandResult, CommandID: pending.CommandID,
				ThreadID: pending.ThreadID, ErrorMessage: errorMessage,
			}}
		}
		return result
	}
	// The native response is empty; the accepted request contains the persisted name.
	pending.Name = choose(pending.Name, lookupString(message, "result", "thread", "name"), lookupString(message, "result", "name"))
	if pending.ThreadID == "" || pending.Name == "" {
		return result
	}
	if remote {
		result.Events = []agentproto.Event{{
			Kind: agentproto.EventThreadNameCommandResult, CommandID: pending.CommandID,
			ThreadID: pending.ThreadID, Name: pending.Name,
		}}
	} else {
		result.Events = []agentproto.Event{{
			Kind: agentproto.EventThreadDiscovered, ThreadID: pending.ThreadID, Name: pending.Name,
		}}
	}
	return result
}
