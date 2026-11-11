package orchestrator

import (
	"strconv"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
)

func asyncQuestionAnswerSections(answers []asyncQuestionAnswer, editable bool) []control.FeishuCardTextSection {
	if len(answers) == 0 {
		return nil
	}
	heading := "你的回答"
	if editable {
		heading = "上次提交的回答"
	}
	const answerBudget = 8192
	total := 0
	for _, answer := range answers {
		total += len(answer.Answer)
	}
	remaining := answerBudget
	lines := make([]string, 0, len(answers)*2)
	for i, answer := range answers {
		prefix := ""
		if len(answers) > 1 {
			prefix = strconv.Itoa(i+1) + ". "
		}
		text, label := answer.Answer, "回答："
		if total > answerBudget {
			quota := remaining / (len(answers) - i)
			if len(text) > quota {
				// boundedAsyncText appends a UTF-8 ellipsis; reserve its bytes so
				// the complete collection of answer excerpts stays in budget.
				text = boundedAsyncText(text, quota-len("…"))
				label = "回答（摘要）："
			}
			remaining -= len(text)
		}
		if editable {
			lines = append(lines, prefix+label+text)
		} else {
			lines = append(lines, prefix+answer.Question, label+text)
		}
	}
	return []control.FeishuCardTextSection{{Label: heading, Lines: lines}}
}
