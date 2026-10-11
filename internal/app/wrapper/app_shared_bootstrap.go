package wrapper

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/jsonrpcutil"
	"github.com/kxn/codex-remote-feishu/internal/debuglog"
)

const sharedBootstrapReplayLimit = 1 << 20

func (r *codexBackendRuntime) BuildSharedStartupResumeFrame() ([]byte, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.sharedBackend || r.sharedInitialResume == "" {
		return nil, "", nil
	}
	threadID := r.sharedInitialResume
	r.sharedInitialResume = ""
	return r.translator.BuildSharedStartupResumeFrame(threadID)
}

// Wait for the protocol subscription before exposing this connection to the
// daemon. Consume the acknowledged response once to establish scope, then
// replay other notifications through stdoutLoop without losing their order.
func (a *App) bootstrapSharedResume(childStdin io.Writer, childStdout io.Reader, rawLogger *debuglog.RawLogger, reportProblem func(agentproto.ErrorInfo)) (io.Reader, error) {
	builder, ok := a.runtime.(interface {
		BuildSharedStartupResumeFrame() ([]byte, string, error)
	})
	if !ok {
		return childStdout, nil
	}
	frame, requestID, err := builder.BuildSharedStartupResumeFrame()
	if err != nil || len(frame) == 0 {
		return childStdout, err
	}
	var request map[string]any
	if err := json.Unmarshal(frame, &request); err != nil {
		return nil, err
	}
	threadID := lookupStringFromMap(request["params"].(map[string]any), "threadId")
	if err := writeChildFrameForRuntime(childStdin, frame, a.runtime, a.debugf, rawLogger, reportProblem); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(childStdout)
	var replay bytes.Buffer
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			if replay.Len()+len(line) > sharedBootstrapReplayLimit {
				return nil, sharedBootstrapScopeProblem(fmt.Errorf("shared resume replay buffer exceeded limit"), threadID)
			}
			var response map[string]any
			if json.Unmarshal(line, &response) == nil && response["id"] == requestID && lookupStringFromMap(response, "method") == "" {
				if message := jsonrpcutil.ExtractErrorMessage(response); message != "" {
					return nil, sharedBootstrapScopeProblem(fmt.Errorf("thread/resume rejected: %s", message), threadID)
				}
				result, _ := response["result"].(map[string]any)
				thread, _ := result["thread"].(map[string]any)
				if lookupStringFromMap(thread, "id") != threadID {
					return nil, sharedBootstrapScopeProblem(fmt.Errorf("thread/resume returned a different or missing thread id"), threadID)
				}
				observed, err := a.runtime.ObserveServer(line)
				if err != nil {
					return nil, sharedBootstrapScopeProblem(err, threadID)
				}
				// Startup resume registers only a protocol subscription. Unexpected
				// actions must fail closed, rather than vanish before relay startup.
				if len(observed.Events)+len(observed.OutboundToChild)+len(observed.OutboundToParent)+len(observed.ResolvedCommandResponses) != 0 {
					return nil, sharedBootstrapScopeProblem(fmt.Errorf("unexpected actions in startup resume response"), threadID)
				}
				return io.MultiReader(bytes.NewReader(replay.Bytes()), reader), nil
			}
			replay.Write(line)
		}
		if readErr != nil {
			return nil, sharedBootstrapScopeProblem(readErr, threadID)
		}
	}
}

func sharedBootstrapScopeProblem(err error, threadID string) agentproto.ErrorInfo {
	return agentproto.ErrorInfo{Code: "shared_backend_scope_failed", Layer: "wrapper", Stage: "shared_backend_bootstrap", Operation: "thread/resume", Message: "共享后端会话订阅失败；请重新连接该会话。", Details: err.Error(), ThreadID: threadID, Retryable: true}
}
