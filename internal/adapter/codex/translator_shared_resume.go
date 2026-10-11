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
	frame, err := json.Marshal(map[string]any{
		"id": requestID, "method": "thread/resume", "params": map[string]any{"threadId": threadID},
	})
	if err != nil {
		return nil, "", err
	}
	frame = append(frame, '\n')
	t.recordSharedOutbound([][]byte{frame}, agentproto.Initiator{})
	return frame, requestID, t.shared.problem
}
