package codex

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

// BuildSharedStartupResumeFrame registers a connection-scoped subscription
// without pretending to be a desktop interaction or modifying backend launch
// settings. The wrapper must consume its response once with ObserveServer.
func (t *Translator) BuildSharedStartupResumeFrame(threadID string) ([]byte, string, error) {
	if t.shared == nil {
		return nil, "", fmt.Errorf("shared startup resume requires shared backend mode")
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, "", fmt.Errorf("shared startup resume requires thread id")
	}
	requestID := t.NextRequest("shared-startup-resume")
	params := map[string]any{"threadId": threadID}
	t.applySharedResumeHistoryPolicy(params)
	frame, err := json.Marshal(map[string]any{
		"id": requestID, "method": "thread/resume", "params": params,
	})
	if err != nil {
		return nil, "", err
	}
	frame = append(frame, '\n')
	t.recordSharedOutbound([][]byte{frame}, agentproto.Initiator{})
	return frame, requestID, t.shared.problem
}

// Resuming subscribes this connection to a thread; history is obtained through
// the paginated history APIs. Hydrating all turns can exceed the shared socket
// frame limit before a prompt or compact command can start. Private stdio keeps
// its existing hydration behavior.
func (t *Translator) applySharedResumeHistoryPolicy(params map[string]any) {
	if t.shared != nil {
		params["excludeTurns"] = true
	}
}
