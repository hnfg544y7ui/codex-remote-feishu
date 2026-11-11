package feishu

import (
	"unicode/utf8"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
)

func fitAsyncQuestionReceiptPage(view control.FeishuPageView, render func(control.FeishuPageView) Operation) Operation {
	operation := render(view)
	if view.PageID != "async-question" || len(view.AsyncQuestionReceiptSections) == 0 {
		return operation
	}
	fits := func(operation Operation) bool {
		return feishuInteractiveMessageTransportFits(renderOperationCard(operation, operation.effectiveCardEnvelope()))
	}
	if fits(operation) {
		return operation
	}
	maxRunes := 0
	for _, section := range view.AsyncQuestionReceiptSections {
		for _, line := range section.Lines {
			if n := utf8.RuneCountInString(line); n > maxRunes {
				maxRunes = n
			}
		}
	}
	for limit := maxRunes / 2; limit > 0; limit /= 2 {
		candidate := control.NormalizeFeishuPageView(view)
		for i, section := range candidate.AsyncQuestionReceiptSections {
			for j, line := range section.Lines {
				runes := []rune(line)
				if len(runes) > limit {
					candidate.AsyncQuestionReceiptSections[i].Lines[j] = string(runes[:limit]) + "…"
				}
			}
		}
		candidate.AsyncQuestionReceiptSections[0].Lines = append([]string{"回答摘要：完整内容较长，以下按题保留摘要。"}, candidate.AsyncQuestionReceiptSections[0].Lines...)
		operation = render(candidate)
		if fits(operation) {
			return operation
		}
	}
	// Preserve the original form/continue controls when even a tiny receipt will
	// not fit. A base page that already overflows keeps the generic fallback.
	candidate := control.NormalizeFeishuPageView(view)
	candidate.AsyncQuestionReceiptSections = nil
	return render(candidate)
}
