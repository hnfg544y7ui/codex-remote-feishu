package feishu

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu/cardtransport"
	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu/projector"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/frontstagecontract"
)

func TestAsyncQuestionReceiptTransportPreservesRequiredInteraction(t *testing.T) {
	for _, mode := range []string{"refill", "continue"} {
		for _, text := range []string{"short", "quotes", "html literals"} {
			t.Run(mode+"/"+text, func(t *testing.T) {
				answer := strings.Repeat("\"", 4096)
				if text == "html literals" {
					answer = strings.Repeat("<>&", 1365) + "<"
				} else if text == "short" {
					answer = "确认本机 <at id=all></at>，补充：仅检查\"网络\""
				}
				page := control.FeishuPageView{PageID: "async-question", Title: "需要你回答",
					Interactive: true, Patchable: true, SuppressDefaultRelatedButtons: true,
					BodySections:   []control.FeishuCardTextSection{{Lines: []string{"请补充测试结果"}}},
					NoticeSections: []control.FeishuCardTextSection{{Lines: []string{"只读确认失败，请重新填写后提交。"}}},
					AsyncQuestionReceiptSections: []control.FeishuCardTextSection{{Label: "上次提交的回答",
						Lines: []string{"1. 回答：" + answer, "2. 回答：" + answer}}},
					AsyncQuestion: &control.AsyncQuestionCardView{ID: "opaque-question-card", Revision: 3,
						CanSubmit: mode == "refill", CanContinue: mode == "continue",
						Questions: []control.AsyncQuestionCardQuestion{{ID: "first", Title: "问题一"}, {ID: "second", Title: "问题二"}}}}
				beforeView, err := json.Marshal(page)
				if err != nil {
					t.Fatal(err)
				}
				render := func(view control.FeishuPageView) Operation {
					return Operation{Kind: OperationUpdateCard,
						SurfaceSessionID: "feishu:bot:topic:existing", CardTitle: view.Title, CardThemeKey: "system", CardUpdateMulti: true,
						CardElements: projector.PageElementsWithOptions(view, "current-daemon", projector.PageRenderOptions{})}
				}
				originalPayload := renderOperationCard(render(page), cardEnvelopeV2)
				before, err := cardtransport.InteractiveMessagePayloadSize(originalPayload)
				if err != nil {
					t.Fatal(err)
				}
				fitted := fitAsyncQuestionReceiptPage(page, render)
				payload := renderOperationCard(fitted, fitted.effectiveCardEnvelope())
				trimmed := trimCardPayloadForMessageTransport(payload)
				after, err := cardtransport.InteractiveMessagePayloadSize(trimmed)
				if err != nil {
					t.Fatal(err)
				}
				forms, callbacks := asyncQuestionReceiptInteractions(trimmed)
				t.Logf("answer bytes=%d transport before=%d after=%d forms=%d callbacks=%v", len(answer)*2, before, after, forms, callbacks)
				if after > cardtransport.InteractiveCardTransportLimitBytes {
					t.Errorf("final card exceeds actual SDK transport envelope: %d", after)
				}
				afterView, err := json.Marshal(page)
				if err != nil || string(beforeView) != string(afterView) {
					t.Error("receipt budget fitting mutated the original view or saved answers")
				}
				if !reflect.DeepEqual(payload, trimmed) {
					t.Error("receipt fitting still required the generic fallback")
				}
				encoded, err := json.Marshal(trimmed)
				if err != nil || !utf8.Valid(encoded) {
					t.Fatal("receipt fitting produced invalid JSON/UTF-8", err)
				}
				if text == "short" {
					if !reflect.DeepEqual(payload, originalPayload) {
						t.Error("short answers should be displayed completely without summary")
					}
				} else if !strings.Contains(string(encoded), "回答摘要") {
					t.Error("overflowing receipt was shortened without a clear summary notice")
				}
				if !strings.Contains(string(encoded), "请补充测试结果") || !strings.Contains(string(encoded), "只读确认失败") {
					t.Error("receipt fitting changed the original intro or actual task status")
				}
				wantCallback := frontstagecontract.CardActionKindAsyncQuestionContinue
				if mode == "refill" {
					wantCallback = frontstagecontract.CardActionKindAsyncQuestionSubmit
					if forms != 1 {
						t.Error("answer receipt removed the required refill form")
					}
				}
				if callbacks[wantCallback] != 1 {
					t.Errorf("answer receipt removed the required %s callback", wantCallback)
				}
			})
		}
	}
}

func asyncQuestionReceiptInteractions(payload map[string]any) (int, map[string]int) {
	forms, callbacks := 0, map[string]int{}
	var walk func(any)
	walk = func(raw any) {
		switch value := raw.(type) {
		case map[string]any:
			if value["tag"] == "form" {
				forms++
			}
			if value["type"] == "callback" {
				if action, ok := value["value"].(map[string]any); ok {
					if kind, ok := action[frontstagecontract.CardActionPayloadKeyKind].(string); ok {
						callbacks[kind]++
					}
				}
			}
			for _, child := range value {
				walk(child)
			}
		case []map[string]any:
			for _, child := range value {
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(payload)
	return forms, callbacks
}
