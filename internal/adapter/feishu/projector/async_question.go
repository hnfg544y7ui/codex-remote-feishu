package projector

import (
	"strconv"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu/cardkit"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/frontstagecontract"
)

func asyncQuestionElements(page control.FeishuPageView, lifecycleID string) []map[string]any {
	view := page.AsyncQuestion
	if view == nil || !page.Interactive || page.Sealed || strings.TrimSpace(lifecycleID) == "" ||
		strings.TrimSpace(view.ID) == "" || len(view.ID) > 512 || view.Revision <= 0 {
		return nil
	}
	payload := func(kind string) map[string]any {
		return stampActionValue(map[string]any{
			frontstagecontract.CardActionPayloadKeyKind:                  kind,
			frontstagecontract.CardActionPayloadKeyAsyncQuestionID:       view.ID,
			frontstagecontract.CardActionPayloadKeyAsyncQuestionRevision: view.Revision,
		}, lifecycleID)
	}
	deferButton := cardCallbackButtonElement("稍后处理", "default", payload(frontstagecontract.CardActionKindAsyncQuestionDefer), false, "")
	if view.CanContinue {
		continueButton := cardCallbackButtonElement("在原会话继续", "primary", payload(frontstagecontract.CardActionKindAsyncQuestionContinue), false, "")
		return []map[string]any{cardkit.ButtonGroupElement([]map[string]any{continueButton, deferButton})}
	}
	if !view.CanSubmit || len(view.Questions) == 0 || len(view.Questions) > 8 {
		return nil
	}
	fields := make([]map[string]any, 0, len(view.Questions)*4+1)
	textBytes := 0
	for index, question := range view.Questions {
		if strings.TrimSpace(question.ID) == "" || len(question.ID) > 512 || strings.TrimSpace(question.Title) == "" || len(question.Title) > 4096 || len(question.Options) > 20 {
			return nil
		}
		textBytes += len(question.Title)
		for _, option := range question.Options {
			textBytes += len(option)
		}
		if textBytes > 8192 {
			return nil
		}
		suffix := strconv.Itoa(index)
		title := question.Title
		if len(view.Questions) > 1 {
			title = strconv.Itoa(index+1) + ". " + title
		}
		if index > 0 {
			fields = append(fields, cardDividerElement())
		}
		fields = append(fields, cardkit.PlainTextBlockElement(title))
		placeholder := "请输入回答"
		if len(question.Options) != 0 {
			options := make([]map[string]any, 0, len(question.Options))
			for choice, text := range question.Options {
				if strings.TrimSpace(text) == "" || len(text) > 1024 {
					return nil
				}
				options = append(options, map[string]any{"text": cardkit.PlainText(text), "value": strconv.Itoa(choice)})
			}
			fields = append(fields, map[string]any{"tag": "select_static", "name": "choice_" + suffix,
				"width": "fill", "placeholder": cardkit.PlainText("请选择"), "options": options})
			placeholder = "补充或其他回答（可选）"
		}
		fields = append(fields, map[string]any{"tag": "input", "name": "answer_" + suffix,
			"width": "fill", "placeholder": cardkit.PlainText(placeholder)})
	}
	submit := cardFormSubmitButtonElement("提交回答", payload(frontstagecontract.CardActionKindAsyncQuestionSubmit))
	submit["name"] = "async_question_submit"
	fields = append(fields, submit)
	// Keep defer outside the form: it must remain an independent callback and
	// never submit or reset the user's draft answers.
	return []map[string]any{
		{"tag": "form", "name": "async_question_answers", "elements": fields},
		cardkit.ButtonGroupElement([]map[string]any{deferButton}),
	}
}
