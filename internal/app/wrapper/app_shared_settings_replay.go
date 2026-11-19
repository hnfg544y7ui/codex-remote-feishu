package wrapper

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func sharedBootstrapSettingsReplay(observed runtimeObserveResult, threadID string) ([]byte, error) {
	if len(observed.OutboundToChild)+len(observed.OutboundToParent)+len(observed.ResolvedCommandResponses) != 0 {
		return nil, fmt.Errorf("unexpected actions in startup resume response")
	}
	var replay bytes.Buffer
	for _, event := range observed.Events {
		settings := event.ThreadSettings
		if event.Kind != agentproto.EventThreadSettingsUpdated || event.ThreadID != threadID || settings == nil || settings.ThreadID != threadID || settings.ApprovalPolicy != "" || settings.Sandbox != "" {
			return nil, fmt.Errorf("unexpected event in startup resume response")
		}
		fields := map[string]any{}
		if settings.Model != "" {
			fields["model"] = settings.Model
		}
		if settings.ModelProviderID != "" {
			fields["modelProvider"] = settings.ModelProviderID
		}
		if settings.ReasoningEffortSet || settings.ReasoningEffort != "" {
			fields["effort"] = nil
			if settings.ReasoningEffort != "" {
				fields["effort"] = settings.ReasoningEffort
			}
		}
		frame, err := json.Marshal(map[string]any{"method": "thread/settings/updated", "params": map[string]any{"threadId": threadID, "threadSettings": fields}})
		if err != nil {
			return nil, err
		}
		replay.Write(frame)
		replay.WriteByte('\n')
	}
	return replay.Bytes(), nil
}
