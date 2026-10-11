package codex

import "github.com/kxn/codex-remote-feishu/internal/core/agentproto"

func (t *Translator) trafficClassForTurn(threadID, turnID string) agentproto.TrafficClass {
	switch {
	case turnID != "" && t.internalTurnIDs[turnID]:
		return agentproto.TrafficClassInternalHelper
	default:
		return agentproto.TrafficClassPrimary
	}
}

func (t *Translator) resolveTurnInitiator(threadID, turnID string, trafficClass agentproto.TrafficClass) agentproto.Initiator {
	if trafficClass == agentproto.TrafficClassInternalHelper {
		return agentproto.Initiator{Kind: agentproto.InitiatorInternalHelper}
	}
	if t.shared != nil {
		if initiator := t.turnInitiators[turnID]; initiator.Kind != "" {
			return initiator
		}
		return agentproto.Initiator{Kind: agentproto.InitiatorLocalUI}
	}
	if surfaceID := t.pendingRemoteTurnByThread[threadID]; surfaceID != "" {
		delete(t.pendingRemoteTurnByThread, threadID)
		delete(t.pendingLocalTurnByThread, threadID)
		return agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: surfaceID}
	}
	if t.pendingLocalTurnByThread[threadID] {
		delete(t.pendingLocalTurnByThread, threadID)
		return agentproto.Initiator{Kind: agentproto.InitiatorLocalUI}
	}
	if turnID != "" {
		if initiator := t.turnInitiators[turnID]; initiator.Kind != "" {
			return initiator
		}
	}
	return agentproto.Initiator{Kind: agentproto.InitiatorUnknown}
}

func (t *Translator) initiatorForTurn(threadID, turnID string) agentproto.Initiator {
	if turnID != "" {
		if initiator := t.turnInitiators[turnID]; initiator.Kind != "" {
			return initiator
		}
	}
	if t.trafficClassForTurn(threadID, turnID) == agentproto.TrafficClassInternalHelper {
		return agentproto.Initiator{Kind: agentproto.InitiatorInternalHelper}
	}
	return agentproto.Initiator{}
}
