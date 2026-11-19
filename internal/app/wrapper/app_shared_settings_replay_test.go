package wrapper

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestSharedStartupSettingsReplayRejectsOtherActionsAndThreads(t *testing.T) {
	for _, observed := range []runtimeObserveResult{
		{Events: []agentproto.Event{{Kind: agentproto.EventTurnStarted, ThreadID: "t"}}},
		{Events: []agentproto.Event{{Kind: agentproto.EventThreadSettingsUpdated, ThreadID: "other", ThreadSettings: &agentproto.ThreadSettingsUpdate{ThreadID: "other", Model: "m"}}}},
		{Events: []agentproto.Event{{Kind: agentproto.EventThreadSettingsUpdated, ThreadID: "t", ThreadSettings: &agentproto.ThreadSettingsUpdate{ThreadID: "t", Model: "m", Sandbox: "danger-full-access"}}}},
		{OutboundToChild: [][]byte{[]byte(`{"method":"turn/start"}`)}},
		{OutboundToParent: [][]byte{[]byte(`{}`)}},
		{ResolvedCommandResponses: []runtimeResolvedCommandResponse{{}}},
	} {
		if _, err := sharedBootstrapSettingsReplay(observed, "t"); err == nil {
			t.Fatalf("unexpected startup action accepted: %#v", observed)
		}
	}
	if replay, err := sharedBootstrapSettingsReplay(runtimeObserveResult{}, "t"); err != nil || len(replay) != 0 {
		t.Fatal("old CLI scope-only response changed")
	}
}
