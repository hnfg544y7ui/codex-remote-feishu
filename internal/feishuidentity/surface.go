package feishuidentity

import "strings"

const (
	PlatformFeishu = "feishu"
	ScopeKindUser  = "user"
	ScopeKindChat  = "chat"
	ScopeKindTopic = "topic"
)

type SurfaceRef struct {
	Platform  string
	GatewayID string
	ScopeKind string
	ScopeID   string
}

func ParseSurfaceRef(surfaceID string) (SurfaceRef, bool) {
	parts := strings.Split(strings.TrimSpace(surfaceID), ":")
	if len(parts) != 4 || parts[0] != PlatformFeishu {
		return SurfaceRef{}, false
	}
	ref := SurfaceRef{
		Platform:  parts[0],
		GatewayID: strings.TrimSpace(parts[1]),
		ScopeKind: strings.TrimSpace(parts[2]),
		ScopeID:   strings.TrimSpace(parts[3]),
	}
	if !ref.valid() {
		return SurfaceRef{}, false
	}
	return ref, true
}

func (r SurfaceRef) SurfaceID() string {
	r.Platform = strings.TrimSpace(r.Platform)
	r.GatewayID = strings.TrimSpace(r.GatewayID)
	r.ScopeKind = strings.TrimSpace(r.ScopeKind)
	r.ScopeID = strings.TrimSpace(r.ScopeID)
	if !r.valid() {
		return ""
	}
	return strings.Join([]string{PlatformFeishu, r.GatewayID, r.ScopeKind, r.ScopeID}, ":")
}

func (r SurfaceRef) IsUser() bool {
	return r.valid() && strings.TrimSpace(r.ScopeKind) == ScopeKindUser
}

func (r SurfaceRef) IsChat() bool {
	return r.valid() && strings.TrimSpace(r.ScopeKind) == ScopeKindChat
}

func (r SurfaceRef) IsTopic() bool {
	return r.valid() && strings.TrimSpace(r.ScopeKind) == ScopeKindTopic
}

func (r SurfaceRef) IsGroup() bool { return r.IsChat() || r.IsTopic() }

// Topic identities use the immutable Feishu root message, never a Codex thread
// or a display title. Including the chat makes forwarded card payloads fail
// closed when their actual callback chat differs.
func TopicRef(gatewayID, chatID, rootMessageID string) SurfaceRef {
	return SurfaceRef{Platform: PlatformFeishu, GatewayID: gatewayID, ScopeKind: ScopeKindTopic,
		ScopeID: strings.TrimSpace(chatID) + "~" + strings.TrimSpace(rootMessageID)}
}

func (r SurfaceRef) TopicChatID() string {
	if !r.IsTopic() {
		return ""
	}
	return strings.Split(r.ScopeID, "~")[0]
}

func (r SurfaceRef) TopicRootMessageID() string {
	if !r.IsTopic() {
		return ""
	}
	return strings.Split(r.ScopeID, "~")[1]
}

func (r SurfaceRef) valid() bool {
	if strings.TrimSpace(r.Platform) != PlatformFeishu {
		return false
	}
	if strings.TrimSpace(r.GatewayID) == "" || strings.TrimSpace(r.ScopeID) == "" {
		return false
	}
	switch strings.TrimSpace(r.ScopeKind) {
	case ScopeKindUser, ScopeKindChat:
		return true
	case ScopeKindTopic:
		parts := strings.Split(strings.TrimSpace(r.ScopeID), "~")
		return len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" &&
			!strings.Contains(r.GatewayID, ":") && !strings.ContainsAny(parts[0]+parts[1], ": \t\r\n")
	default:
		return false
	}
}
