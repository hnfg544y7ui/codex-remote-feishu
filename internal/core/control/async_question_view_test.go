package control

import "testing"

func TestNormalizeAsyncQuestionPageDeepClonesOnlyQuestionMetadata(t *testing.T) {
	input := &AsyncQuestionCardView{ID: "opaque-card", Revision: 3, CanSubmit: true,
		Questions: []AsyncQuestionCardQuestion{{ID: "source-question", Title: "Choose", Options: []string{"one", "two"}}}}
	page := NormalizeFeishuPageView(FeishuPageView{PageID: "async-question", Interactive: true, AsyncQuestion: input})
	if page.AsyncQuestion == nil || page.AsyncQuestion == input || page.AsyncQuestion.ID != input.ID || !page.Interactive {
		t.Fatal("normalization lost or aliased the question card", page)
	}
	page.AsyncQuestion.Questions[0].Title = "changed"
	page.AsyncQuestion.Questions[0].Options[0] = "changed"
	if input.Questions[0].Title != "Choose" || input.Questions[0].Options[0] != "one" {
		t.Fatal("normalized metadata mutated its source")
	}
	if NormalizeFeishuPageView(FeishuPageView{}).AsyncQuestion != nil {
		t.Fatal("ordinary pages acquired an implicit question form")
	}
}

func TestNormalizeAsyncQuestionPageDeepClonesAnswerReceipt(t *testing.T) {
	input := FeishuPageView{PageID: "async-question", Sealed: true,
		BodySections:                 []FeishuCardTextSection{{Lines: []string{"解释"}}},
		AsyncQuestionReceiptSections: []FeishuCardTextSection{{Label: "你的回答", Lines: []string{"原始回答"}}}}
	page := NormalizeFeishuPageView(input)
	if len(page.AsyncQuestionReceiptSections) != 1 || len(page.BodySections) != 1 || page.AsyncQuestion != nil || !page.Sealed {
		t.Fatal("read-only receipt was lost or acquired an implicit answer form")
	}
	page.AsyncQuestionReceiptSections[0].Label = "changed"
	page.AsyncQuestionReceiptSections[0].Lines[0] = "truncated display"
	if input.AsyncQuestionReceiptSections[0].Label != "你的回答" || input.AsyncQuestionReceiptSections[0].Lines[0] != "原始回答" {
		t.Fatal("display adaptation changed the source receipt")
	}
	if NormalizeFeishuPageView(FeishuPageView{}).AsyncQuestionReceiptSections != nil {
		t.Fatal("ordinary page acquired an implicit answer receipt")
	}
}
