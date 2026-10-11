package codex

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/jsonrpcutil"
)

func (t *Translator) observeSharedServer(raw []byte) (Result, error) {
	defer t.shared.pruneNativeHistory()
	if err := t.CheckSharedBackendTimeout(time.Now()); err != nil {
		return Result{Suppress: true}, err
	}
	var message map[string]any
	if err := json.Unmarshal(raw, &message); err != nil {
		return Result{Suppress: true}, err
	}
	method := lookupString(message, "method")
	if method == "" && sharedRequestKey(message["id"]) != "" {
		return t.observeSharedResponse(raw, message)
	}
	threadID, turnID := sharedMessageThread(message), sharedMessageTurn(message)
	if threadID != "" && !t.shared.threads[threadID] {
		if t.sharedHasPendingThreadCreate() {
			return t.bufferSharedFrame(raw)
		}
		return Result{Suppress: true}, nil
	}
	if turnID != "" && t.turnInitiators[turnID].Kind == "" && t.sharedThreadHasPendingTurn(threadID) {
		return t.bufferSharedFrame(raw)
	}
	if method == "serverRequest/started" || method == "request/started" {
		// Broadcast observability is not a native JSON-RPC request that this
		// client can answer. In particular it must not steal desktop callbacks.
		return Result{Suppress: true}, nil
	}
	if method == "serverRequest/resolved" || method == "request/resolved" {
		requestID := extractRequestID(message, extractRequestPayload(message))
		if !t.shared.seenNative[requestID] || t.shared.nativeResolved[requestID] {
			return Result{Suppress: true}, nil
		}
		t.shared.nativeResolved[requestID] = true
		delete(t.shared.nativeRequests, requestID)
		delete(t.shared.nativeTurns, requestID)
		if t.shared.nativeOwners[requestID].Kind == agentproto.InitiatorLocalUI {
			return Result{}, nil
		}
	}
	if sharedRequestKey(message["id"]) != "" && method != "" {
		return t.observeSharedNativeRequest(raw, message, turnID)
	}
	if turnID != "" && t.turnInitiators[turnID].Kind == "" {
		t.turnInitiators[turnID] = agentproto.Initiator{Kind: agentproto.InitiatorLocalUI}
	}
	if method == "turn/started" && (t.shared.startedTurns[turnID] || t.shared.completedTurns[turnID]) {
		return Result{Suppress: true}, nil
	}
	if method == "turn/completed" && t.shared.completedTurns[turnID] {
		return Result{Suppress: true}, nil
	}
	selected := t.shared.selectedThread
	result, err := t.observeServer(raw)
	if method == "thread/started" && threadID != selected {
		t.currentThreadID = selected
	}
	if err == nil {
		if method == "turn/started" && turnID != "" {
			t.shared.startedTurns[turnID] = true
		}
		if method == "turn/completed" && turnID != "" {
			for requestID, requestTurn := range t.shared.nativeTurns {
				if requestTurn == turnID {
					delete(t.shared.nativeRequests, requestID)
					delete(t.shared.nativeTurns, requestID)
				}
			}
			t.shared.completedTurns[turnID] = true
			t.shared.completedOrder = append(t.shared.completedOrder, turnID)
			if len(t.shared.completedOrder) > 512 {
				old := t.shared.completedOrder[0]
				t.shared.completedOrder = t.shared.completedOrder[1:]
				delete(t.shared.completedTurns, old)
				delete(t.shared.startedTurns, old)
				delete(t.shared.ownTurns, old)
				delete(t.turnInitiators, old)
			}
		}
		t.recordSharedOutbound(result.OutboundToCodex, agentproto.Initiator{})
	}
	return result, err
}

func (t *Translator) observeSharedResponse(raw []byte, message map[string]any) (Result, error) {
	key := sharedRequestKey(message["id"])
	request, own := t.shared.requests[key]
	if !own {
		// A shared transport must never consume another client's response or a
		// delayed response from an earlier connection generation.
		return Result{Suppress: true}, nil
	}
	delete(t.shared.requests, key)
	if jsonrpcutil.ExtractErrorMessage(message) == "" {
		switch request.method {
		case "thread/start", "thread/resume", "thread/fork":
			threadID := lookupString(message, "result", "thread", "id")
			if threadID == "" {
				return Result{Suppress: true}, fmt.Errorf("shared backend %s response has no thread id; reconnect this client", request.method)
			}
			if request.initiator.Kind == agentproto.InitiatorInternalHelper {
				t.shared.threads[threadID] = true
			} else {
				t.shared.threads = map[string]bool{threadID: true}
				t.shared.selectedThread, t.currentThreadID = threadID, threadID
			}
		case "turn/start", "review/start":
			turnID := lookupString(message, "result", "turn", "id")
			if turnID == "" {
				return Result{Suppress: true}, fmt.Errorf("shared backend %s response has no turn id; reconnect this client", request.method)
			}
			t.turnInitiators[turnID] = request.initiator
			t.shared.ownTurns[turnID] = true
			if request.method == "review/start" {
				if threadID := lookupString(message, "result", "reviewThreadId"); threadID != "" {
					t.shared.threads[threadID] = true
				}
			}
			if request.initiator.Kind == agentproto.InitiatorInternalHelper {
				t.internalTurnIDs[turnID] = true
			}
		}
	}
	result, err := t.observeServer(raw)
	if err != nil {
		return result, err
	}
	t.recordSharedOutbound(result.OutboundToCodex, request.initiator)
	buffered := t.shared.buffered
	t.shared.buffered, t.shared.bufferedBytes = nil, 0
	for _, frame := range buffered {
		flushed, err := t.observeSharedServer(frame)
		if err != nil {
			return result, err
		}
		result.Events = append(result.Events, flushed.Events...)
		result.OutboundToCodex = append(result.OutboundToCodex, flushed.OutboundToCodex...)
		result.OutboundToParent = append(result.OutboundToParent, flushed.OutboundToParent...)
		// Buffered native frames or local notifications must reach their parent
		// even though the original frame was suppressed while ownership settled.
		if !flushed.Suppress {
			result.OutboundToParent = append(result.OutboundToParent, frame)
		}
	}
	return result, nil
}

func (t *Translator) observeSharedNativeRequest(raw []byte, message map[string]any, turnID string) (Result, error) {
	if turnID == "" {
		return Result{Suppress: true}, fmt.Errorf("shared backend native request has no provable turn owner; reconnect this client or handle it in the backend's desktop client")
	}
	if t.shared.completedTurns[turnID] {
		return Result{Suppress: true}, nil
	}
	owner := t.turnInitiators[turnID]
	if owner.Kind != agentproto.InitiatorRemoteSurface && owner.Kind != agentproto.InitiatorLocalUI {
		return Result{Suppress: true}, nil
	}
	// LocalUI only proves an outside turn unless this connection sent the
	// request whose response established it. No desktop approval may be
	// answered by a headless observing connection.
	if owner.Kind == agentproto.InitiatorLocalUI && !t.shared.ownsLocalTurn(turnID) {
		return Result{Suppress: true}, nil
	}
	requestID := extractRequestID(message, nil)
	if requestID == "" {
		return Result{Suppress: true}, fmt.Errorf("shared backend native request has no valid request id")
	}
	if t.shared.seenNative[requestID] {
		return Result{Suppress: true}, nil
	}
	if len(t.shared.nativeRequests) >= 64 {
		return Result{Suppress: true}, fmt.Errorf("shared backend has too many pending native requests; reconnect this client")
	}
	t.shared.seenNative[requestID] = true
	t.shared.nativeOwners[requestID] = owner
	t.shared.nativeOrder = append(t.shared.nativeOrder, requestID)
	t.shared.nativeRequests[requestID] = owner
	t.shared.nativeTurns[requestID] = turnID
	if owner.Kind == agentproto.InitiatorLocalUI {
		return Result{}, nil
	}
	result, err := t.observeServer(raw)
	result.Suppress = true
	return result, err
}
