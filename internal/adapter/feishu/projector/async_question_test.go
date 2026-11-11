package projector

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/frontstagecontract"
)

func TestAsyncQuestionFormLayoutSeparatesNumberedTitlesFromFullWidthFields(t *testing.T) {
	page := control.FeishuPageView{Interactive: true, AsyncQuestion: &control.AsyncQuestionCardView{
		ID: "question-card", Revision: 3, CanSubmit: true,
		Questions: []control.AsyncQuestionCardQuestion{
			{ID: "free", Title: "First <at id=all></at>"},
			{ID: "choice", Title: "Second", Options: []string{"第一项", "第二项"}},
		},
	}}
	elements := asyncQuestionElements(page, "current-daemon")
	if len(elements) != 2 || elements[0]["tag"] != "form" {
		t.Fatal("expected answer form followed by independent defer action", elements)
	}
	fields := elements[0]["elements"].([]map[string]any)
	wantTags := []string{"div", "input", "hr", "div", "select_static", "input", "button"}
	if len(fields) != len(wantTags) {
		t.Fatal("question layout contains repeated labels or unexpected rows", fields)
	}
	for i, tag := range wantTags {
		if fields[i]["tag"] != tag {
			t.Fatalf("element %d: got %v, want %s", i, fields[i]["tag"], tag)
		}
	}
	for index, title := range map[int]string{0: "1. First <at id=all></at>", 3: "2. Second"} {
		text := fields[index]["text"].(map[string]any)
		if text["tag"] != "plain_text" || text["content"] != title {
			t.Fatal("question title must appear once as an inert numbered block", fields[index])
		}
	}
	for index, name := range map[int]string{1: "answer_0", 4: "choice_1", 5: "answer_1"} {
		field := fields[index]
		if field["name"] != name || field["width"] != "fill" || field["label"] != nil || field["label_position"] != nil {
			t.Fatal("full-width field acquired a duplicate side label or changed callback name", field)
		}
	}
	for index, placeholder := range map[int]string{1: "请输入回答", 4: "请选择", 5: "补充或其他回答（可选）"} {
		text := fields[index]["placeholder"].(map[string]any)
		if text["tag"] != "plain_text" || text["content"] != placeholder {
			t.Fatal("question field has an unclear or repeated placeholder", fields[index])
		}
	}
	submit := fields[6]
	if submit["name"] != "async_question_submit" || submit["form_action_type"] != "submit" {
		t.Fatal("layout changed native form submit semantics", submit)
	}
	deferButton := elements[1]
	if deferButton["tag"] != "button" || deferButton["form_action_type"] != nil || deferButton["name"] != nil {
		t.Fatal("deferral entered the form submission/reset route", deferButton)
	}
	for _, button := range []map[string]any{submit, deferButton} {
		behavior := button["behaviors"].([]map[string]any)[0]
		value := behavior["value"].(map[string]any)
		if value[frontstagecontract.CardActionPayloadKeyAsyncQuestionID] != "question-card" ||
			value[frontstagecontract.CardActionPayloadKeyAsyncQuestionRevision] != 3 ||
			value[frontstagecontract.CardActionPayloadKeyDaemonLifecycleID] != "current-daemon" {
			t.Fatal("layout changed callback identity or freshness", value)
		}
	}
}

func TestAsyncQuestionSingleQuestionHasOneStandaloneTitle(t *testing.T) {
	elements := asyncQuestionElements(control.FeishuPageView{Interactive: true,
		AsyncQuestion: &control.AsyncQuestionCardView{ID: "one-question", Revision: 1, CanSubmit: true,
			Questions: []control.AsyncQuestionCardQuestion{{ID: "free", Title: "确认测试结果"}}}}, "current-daemon")
	fields := elements[0]["elements"].([]map[string]any)
	if len(fields) != 3 || fields[0]["tag"] != "div" || fields[1]["tag"] != "input" || fields[2]["tag"] != "button" {
		t.Fatal("single question should have one title, one full-width answer and submit", fields)
	}
	if fields[0]["text"].(map[string]any)["content"] != "确认测试结果" || fields[1]["label"] != nil {
		t.Fatal("single question title was numbered or repeated as an input label", fields)
	}
}
