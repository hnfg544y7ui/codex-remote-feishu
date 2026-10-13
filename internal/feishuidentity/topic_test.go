package feishuidentity

import "testing"

func TestTopicRefRoundTripKeepsNativeNamespacesDistinct(t *testing.T) {
	ref := TopicRef("app-1", "oc_chat", "om_root")
	parsed, ok := ParseSurfaceRef(ref.SurfaceID())
	if !ok || !parsed.IsTopic() || !parsed.IsGroup() || parsed.IsChat() || parsed.IsUser() || parsed.TopicChatID() != "oc_chat" || parsed.TopicRootMessageID() != "om_root" {
		t.Fatalf("topic roundtrip %#v %t", parsed, ok)
	}
	for _, id := range []string{"feishu:app-1:topic:oc_chat", "feishu:app-1:topic:~om_root", "feishu:app-1:topic:oc_chat~", "feishu:app-1:topic:oc_chat~om_root~extra", "feishu:app-1:topic:oc_chat~om root", "feishu:app-1:topic:oc_chat~om_root:extra"} {
		if _, ok := ParseSurfaceRef(id); ok {
			t.Fatalf("accepted invalid topic scope %q", id)
		}
	}
}

func TestTopicRefRejectsStructuralGatewayDelimiter(t *testing.T) {
	if TopicRef("bot:other", "oc_chat", "om_root").SurfaceID() != "" {
		t.Fatal("accepted ambiguous gateway")
	}
	if ref := TopicRef("中文 bot", "oc_chat", "om_root"); ref.SurfaceID() == "" {
		t.Fatal("rejected display name with space")
	}
}
