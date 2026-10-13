package codex

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestAsyncQuestionNotificationPreservesDeliveryWithoutCreatingRPCRequest(t *testing.T) {
	tr := NewTranslator("inst")
	result, err := tr.ObserveServer([]byte(`{"method":"item/completed","params":{"threadId":"thread","turnId":"turn","item":{"id":"call-question","type":"agentMessage","text":"是否允许升级？","phase":"final_answer","delivery":"async","questions":[{"title":"是否允许升级？","options":["允许","暂不"]}]}}}`))
	if err != nil || len(result.Events) != 1 {
		t.Fatalf("unexpected result: %+v %v", result, err)
	}
	event := result.Events[0]
	if event.Kind != agentproto.EventItemCompleted || event.RequestID != "" || event.RequestPrompt != nil || event.Metadata["messageDelivery"] != "async" || event.Metadata["hasAsyncQuestions"] != true {
		t.Fatalf("async question was lost or converted to an RPC request: %+v", event)
	}
}
