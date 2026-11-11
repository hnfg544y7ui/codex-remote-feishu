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
