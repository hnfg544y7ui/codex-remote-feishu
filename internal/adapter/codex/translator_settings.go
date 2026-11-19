package codex

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/jsonrpcutil"
)

type pendingThreadSettingsSet struct {
	CommandID string
	ThreadID  string
}

func (t *Translator) translateThreadSettingsSet(command agentproto.Command) ([][]byte, error) {
	threadID := strings.TrimSpace(command.Target.ThreadID)
	update := command.ThreadSettingsSet
	if command.CommandID == "" || threadID == "" || update == nil || (update.Model == nil && update.ReasoningEffort == nil) {
		return nil, fmt.Errorf("thread.settings.set requires command id, target thread and an explicit field")
	}
	params := map[string]any{"threadId": threadID}
	if update.Model != nil {
		model := strings.TrimSpace(*update.Model)
		if model == "" {
			return nil, fmt.Errorf("thread.settings.set requires a nonempty model")
		}
		params["model"] = model
	}
	if update.ReasoningEffort != nil {
		effort := strings.TrimSpace(*update.ReasoningEffort)
		if effort == "" {
			// Native 0.170 accepts null but treats it as no change. Do not
			// report a successful reset when the existing effort is retained.
			return nil, fmt.Errorf("thread.settings.set requires an explicit reasoning effort; native auto reset is unsupported")
		}
		params["effort"] = effort
	}
	requestID := t.NextRequest("thread-settings-set")
	frame, err := json.Marshal(map[string]any{"id": requestID, "method": "thread/settings/update", "params": params})
	if err != nil {
		return nil, err
	}
	t.pendingThreadSettingsSet[requestID] = pendingThreadSettingsSet{CommandID: command.CommandID, ThreadID: threadID}
	return [][]byte{append(frame, '\n')}, nil
}

func threadSettingsSetResult(pending pendingThreadSettingsSet, message map[string]any) Result {
	event := agentproto.Event{Kind: agentproto.EventThreadSettingsSetResult, CommandID: pending.CommandID, ThreadID: pending.ThreadID}
	_, valid := message["result"].(map[string]any)
	if _, failed := message["error"]; failed || !valid {
		event.ErrorMessage = jsonrpcutil.ExtractErrorMessage(message)
		if event.ErrorMessage == "" {
			event.ErrorMessage = "Codex returned an invalid thread/settings/update response"
		}
	}
	// An empty ACK does not carry authoritative settings. Only native settings
	// notifications (or a subsequent read) project the actual model and effort.
	return Result{Suppress: true, Events: []agentproto.Event{event}}
}
