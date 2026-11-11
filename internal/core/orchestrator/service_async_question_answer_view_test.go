package orchestrator

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAsyncQuestionAnswerSectionsShowActualAnswersWithoutChangingInput(t *testing.T) {
	answers := []asyncQuestionAnswer{
		{QuestionItemID: "choice", Question: "*选择* <at id=all></at>", Answer: "同意\n补充：只测试本机"},
		{QuestionItemID: "text", Question: "检查哪些内容？", Answer: "网络和服务"},
	}
	before := append([]asyncQuestionAnswer(nil), answers...)
	sections := asyncQuestionAnswerSections(answers, false)
	if len(sections) != 1 || sections[0].Label != "你的回答" {
		t.Fatal("answer receipt must use a static heading", sections)
	}
	want := []string{"1. *选择* <at id=all></at>", "回答：同意\n补充：只测试本机", "2. 检查哪些内容？", "回答：网络和服务"}
	if !reflect.DeepEqual(sections[0].Lines, want) {
		t.Fatal("question and actual option/supplement answers were lost or mismatched", sections)
	}
	sections[0].Lines[0] = "changed rendered section"
	if !reflect.DeepEqual(answers, before) {
		t.Fatal("rendering changed the original answer payload", answers)
	}
}

func TestAsyncQuestionAnswerSectionsEditableOnlyShowsPreviousAnswers(t *testing.T) {
	answers := []asyncQuestionAnswer{
		{Question: "下方表单中的题目一", Answer: "上次回答一"},
		{Question: "下方表单中的题目二", Answer: "上次回答二"},
	}
	sections := asyncQuestionAnswerSections(answers, true)
	if len(sections) != 1 || sections[0].Label != "上次提交的回答" ||
		!reflect.DeepEqual(sections[0].Lines, []string{"1. 回答：上次回答一", "2. 回答：上次回答二"}) {
		t.Fatal("editable receipt should not repeat questions from the form", sections)
	}
}

func TestAsyncQuestionAnswerSectionsPreserveCompleteAnswersThroughBudget(t *testing.T) {
	first, second := strings.Repeat("x", 4096), strings.Repeat("中", 1365)+"!"
	sections := asyncQuestionAnswerSections([]asyncQuestionAnswer{{Question: "一", Answer: first}, {Question: "二", Answer: second}}, false)
	if sections[0].Lines[1] != "回答："+first || sections[0].Lines[3] != "回答："+second ||
		strings.Contains(strings.Join(sections[0].Lines, "\n"), "摘要") {
		t.Fatal("answers at the 8192-byte boundary must be shown in full")
	}
}

func TestAsyncQuestionAnswerSectionsSummarizeLongChineseAndRetainEveryQuestion(t *testing.T) {
	for _, editable := range []bool{false, true} {
		answers := make([]asyncQuestionAnswer, 8)
		for i := range answers {
			answers[i] = asyncQuestionAnswer{QuestionItemID: strconv.Itoa(i), Question: "题目" + strconv.Itoa(i+1), Answer: strings.Repeat("完整回答", 1024)}
		}
		before := append([]asyncQuestionAnswer(nil), answers...)
		sections := asyncQuestionAnswerSections(answers, editable)
		answerBytes := 0
		for i := range answers {
			lineIndex := i*2 + 1
			prefix := "回答（摘要）："
			if editable {
				lineIndex = i
				prefix = strconv.Itoa(i+1) + ". " + prefix
			} else if sections[0].Lines[i*2] != strconv.Itoa(i+1)+". "+answers[i].Question {
				t.Fatal("long-answer receipt dropped or reordered a question", i, sections)
			}
			line := sections[0].Lines[lineIndex]
			if !utf8.ValidString(line) || !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, "…") {
				t.Fatal("truncated answer must be UTF-8 safe and visibly marked as a summary", line)
			}
			answerBytes += len(strings.TrimPrefix(line, prefix))
		}
		if answerBytes > 8192 || !reflect.DeepEqual(answers, before) {
			t.Fatal("summary exceeded its budget or changed the source answer payload", answerBytes)
		}
	}
}

func TestAsyncQuestionAnswerSectionsEmptyAndSingle(t *testing.T) {
	if got := asyncQuestionAnswerSections(nil, false); got != nil {
		t.Fatal("no submitted answers should render no receipt", got)
	}
	got := asyncQuestionAnswerSections([]asyncQuestionAnswer{{Question: "题目", Answer: "回答内容"}}, false)
	if !reflect.DeepEqual(got[0].Lines, []string{"题目", "回答：回答内容"}) {
		t.Fatal("single-answer receipt should avoid unnecessary numbering", got)
	}
}
