package agentproto

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestAsyncQuestionEventWireRoundtripDoesNotAddRequestSemantics(t *testing.T) {
	want := Event{Kind: EventItemCompleted, ThreadID: "thread", TurnID: "turn", ItemID: "native-question",
		AsyncQuestions: []AsyncQuestion{{ID: "source-question", Title: "选择继续方式", Options: []string{"继续", "暂不"}}}}
	encoded, err := MarshalEnvelope(Envelope{Type: EnvelopeEventBatch, EventBatch: &EventBatch{Events: []Event{want}}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded Envelope
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.EventBatch == nil || len(decoded.EventBatch.Events) != 1 {
		t.Fatal("event batch lost async question", string(encoded))
	}
	got := decoded.EventBatch.Events[0]
	if !reflect.DeepEqual(got.AsyncQuestions, want.AsyncQuestions) || got.RequestID != "" || got.RequestPrompt != nil {
		t.Fatal("question identity changed or acquired fake RPC semantics", got)
	}
}
