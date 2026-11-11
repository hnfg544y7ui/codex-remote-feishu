package feishu

import (
	"context"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
)

func TestAsyncQuestionCallbacksAckBeforeHandlerCompletes(t *testing.T) {
	for _, kind := range []control.ActionKind{control.ActionAsyncQuestionSubmit, control.ActionAsyncQuestionContinue, control.ActionAsyncQuestionDefer} {
		t.Run(string(kind), func(t *testing.T) {
			action := control.Action{Kind: kind, Inbound: &control.ActionInboundMeta{CardCallback: true, CardDaemonLifecycleID: "current"}}
			if !control.RejectsUnstampedFeishuCardCallback(control.Action{Kind: kind, Inbound: &control.ActionInboundMeta{CardCallback: true}}) {
				t.Fatal("unstamped form can bypass lifecycle gate")
			}
			started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			defer close(release)
			go func() {
				response, err := handleCardActionTrigger(context.Background(), action, func(context.Context, control.Action) *ActionResult { close(started); <-release; return nil })
				if err != nil || response == nil || response.Card != nil {
					t.Error("async question must return an empty ACK, not a stale synchronous replacement")
				}
				close(done)
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("handler not started")
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("platform ACK waited for native or Feishu REST work")
			}
		})
	}
}
