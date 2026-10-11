package codex

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

const sharedBackendOwnershipTimeout = 15 * time.Second

type sharedBackendRequest struct {
	method    string
	threadID  string
	initiator agentproto.Initiator
	startedAt time.Time
}

type sharedBackendState struct {
	requests       map[string]sharedBackendRequest
	threads        map[string]bool
	selectedThread string
	buffered       [][]byte
	bufferedBytes  int
	nativeRequests map[string]agentproto.Initiator
	nativeTurns    map[string]string
	seenNative     map[string]bool
	nativeOwners   map[string]agentproto.Initiator
	nativeResolved map[string]bool
	nativeOrder    []string
	ownTurns       map[string]bool
	startedTurns   map[string]bool
	completedTurns map[string]bool
	completedOrder []string
	problem        error
}

// SetSharedBackend opts into connection-scoped ownership. Private stdio keeps
// its original protocol semantics unless explicitly enabled by the wrapper.
func (t *Translator) SetSharedBackend(enabled bool) {
	if !enabled {
		t.shared = nil
		return
	}
	t.shared = &sharedBackendState{
		requests:       map[string]sharedBackendRequest{},
		threads:        map[string]bool{},
		nativeRequests: map[string]agentproto.Initiator{},
		nativeTurns:    map[string]string{},
		seenNative:     map[string]bool{},
		nativeOwners:   map[string]agentproto.Initiator{},
		nativeResolved: map[string]bool{},
		ownTurns:       map[string]bool{},
		startedTurns:   map[string]bool{},
		completedTurns: map[string]bool{},
	}
}

func (t *Translator) checkSharedParentResponse(raw []byte) error {
	if t.shared == nil {
		return nil
	}
	if t.shared.problem != nil {
		return t.shared.problem
	}
	var message map[string]any
	if json.Unmarshal(raw, &message) != nil || lookupString(message, "method") != "" || message["id"] == nil {
		return nil
	}
	requestID := extractRequestID(message, nil)
	owner, ok := t.shared.nativeRequests[requestID]
	if !ok || owner.Kind != agentproto.InitiatorLocalUI {
		return fmt.Errorf("shared backend native response is not owned by this desktop connection")
	}
	delete(t.shared.nativeRequests, requestID)
	delete(t.shared.nativeTurns, requestID)
	t.shared.pruneNativeHistory()
	return nil
}

// Pending native requests keep their ownership and deduplication evidence until
// consumed. Only completed history is evicted; pending entries have their own
// 64-request limit, separate from the 512-entry completed history limit.
func (s *sharedBackendState) pruneNativeHistory() {
	excess := len(s.nativeOrder) - len(s.nativeRequests) - 512
	if excess <= 0 {
		return
	}
	retained := s.nativeOrder[:0]
	for _, requestID := range s.nativeOrder {
		_, pending := s.nativeRequests[requestID]
		if excess > 0 && !pending {
			delete(s.seenNative, requestID)
			delete(s.nativeOwners, requestID)
			delete(s.nativeResolved, requestID)
			excess--
			continue
		}
		retained = append(retained, requestID)
	}
	s.nativeOrder = retained
}

func (s *sharedBackendState) ownsLocalTurn(turnID string) bool { return s.ownTurns[turnID] }

// ResetSharedConnection drops connection-local request and turn ownership. The
// caller may then initialize and resume the selected thread, but never replay
// a previous turn/start. Keep request IDs monotonic across the reconnect.
func (t *Translator) ResetSharedConnection() {
	if t.shared == nil {
		return
	}
	threadID, cwd, base := t.currentThreadID, t.knownThreadCWD[t.currentThreadID], t.TranslatorBase
	fresh := NewTranslator(t.instanceID)
	fresh.TranslatorBase = base
	fresh.currentThreadID = threadID
	if threadID != "" {
		fresh.knownThreadCWD[threadID] = cwd
	}
	*t = *fresh
	t.SetSharedBackend(true)
}

// CheckSharedBackendTimeout must be polled by the connection lifecycle even
// when the peer sends no frames. An unresolved ownership handshake requires a
// reconnect; it never implies that an external backend's turn has ended.
func (t *Translator) CheckSharedBackendTimeout(now time.Time) error {
	if t.shared == nil {
		return nil
	}
	if t.shared.problem != nil {
		return t.shared.problem
	}
	for _, request := range t.shared.requests {
		switch request.method {
		case "thread/start", "thread/resume", "thread/fork", "turn/start", "review/start":
			if now.Sub(request.startedAt) >= sharedBackendOwnershipTimeout {
				return fmt.Errorf("shared backend ownership response timed out for %s; reconnect this client", request.method)
			}
		}
	}
	return nil
}

func sharedRequestKey(id any) string {
	if id == nil {
		return ""
	}
	encoded, err := json.Marshal(id)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func sharedMessageThread(message map[string]any) string {
	return choose(lookupString(message, "params", "threadId"), lookupString(message, "params", "thread", "id"),
		lookupString(message, "params", "request", "threadId"))
}

func sharedMessageTurn(message map[string]any) string {
	return choose(lookupString(message, "params", "turnId"), lookupString(message, "params", "turn", "id"),
		lookupString(message, "params", "request", "turnId"))
}

func (t *Translator) recordSharedOutbound(frames [][]byte, initiator agentproto.Initiator) {
	if t.shared == nil {
		return
	}
	for _, raw := range frames {
		var message map[string]any
		if json.Unmarshal(raw, &message) != nil {
			continue
		}
		method := lookupString(message, "method")
		key := sharedRequestKey(message["id"])
		if method == "" || key == "" {
			continue
		}
		if len(t.shared.requests) >= 256 {
			t.shared.problem = fmt.Errorf("shared backend has too many unresolved requests; reconnect this client")
			return
		}
		threadID := lookupString(message, "params", "threadId")
		owner := initiator
		if owner.Kind == "" {
			if surfaceID := t.pendingRemoteTurnByThread[threadID]; surfaceID != "" {
				owner = agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: surfaceID}
			}
			if pending, ok := t.pendingThreadResume[fmt.Sprint(message["id"])]; ok {
				owner = agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: pending.Command.Origin.Surface}
			}
		}
		if (method == "turn/start" && t.pendingInternalTurnSet[fmt.Sprint(message["id"])]) ||
			(method == "thread/start" && t.pendingInternalThreadSet[fmt.Sprint(message["id"])]) {
			owner = agentproto.Initiator{Kind: agentproto.InitiatorInternalHelper}
		}
		t.shared.requests[key] = sharedBackendRequest{method: method, threadID: threadID, initiator: owner, startedAt: time.Now()}
	}
}

func (t *Translator) sharedThreadHasPendingTurn(threadID string) bool {
	for _, request := range t.shared.requests {
		if request.threadID == threadID && (request.method == "turn/start" || request.method == "review/start") {
			return true
		}
	}
	return false
}

func (t *Translator) sharedHasPendingThreadCreate() bool {
	for _, request := range t.shared.requests {
		if request.method == "thread/start" || request.method == "thread/fork" || request.method == "thread/resume" || request.method == "review/start" {
			return true
		}
	}
	return false
}

func (t *Translator) bufferSharedFrame(raw []byte) (Result, error) {
	if len(t.shared.buffered) >= 128 || t.shared.bufferedBytes+len(raw) > 1024*1024 {
		return Result{Suppress: true}, fmt.Errorf("shared backend ownership buffer exceeded; reconnect this client")
	}
	t.shared.buffered = append(t.shared.buffered, append([]byte(nil), raw...))
	t.shared.bufferedBytes += len(raw)
	return Result{Suppress: true}, nil
}

func (t *Translator) sharedRequestRespondAllowed(command agentproto.Command) error {
	if t.shared == nil || command.Kind != agentproto.CommandRequestRespond {
		return nil
	}
	owner, ok := t.shared.nativeRequests[strings.TrimSpace(command.Request.RequestID)]
	if !ok || owner.Kind != agentproto.InitiatorRemoteSurface ||
		(owner.SurfaceSessionID != "" && owner.SurfaceSessionID != command.Origin.Surface) {
		return fmt.Errorf("shared backend request is not owned by this remote surface; refresh or reconnect")
	}
	return nil
}
