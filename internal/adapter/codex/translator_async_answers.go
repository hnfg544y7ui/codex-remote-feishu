package codex

import (
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func extractAsyncQuestionAnswers(kind string, item map[string]any) []agentproto.AsyncQuestionAnswer {
	if kind != "user_message" {
		return nil
	}
	text := strings.TrimSpace(extractItemText(item))
	const open, close = "<send_user_message_question_reply>", "</send_user_message_question_reply>"
	if len(text) > 256*1024 || !strings.HasPrefix(text, open) || !strings.HasSuffix(text, close) {
		return nil
	}
	var replies []struct {
		QuestionItemID string `json:"questionItemId"`
		Question       string `json:"question"`
		Answer         string `json:"answer"`
	}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSuffix(strings.TrimPrefix(text, open), close)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&replies) != nil || len(replies) == 0 || len(replies) > maxAsyncQuestions {
		return nil
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil
	}
	seen, total := map[string]bool{}, 0
	answers := make([]agentproto.AsyncQuestionAnswer, 0, len(replies))
	for _, reply := range replies {
		if !validAsyncQuestionText(reply.QuestionItemID, maxAsyncQuestionID) || seen[reply.QuestionItemID] ||
			!validAsyncQuestionText(reply.Answer, 5120) || len(reply.Question) > maxAsyncQuestionTitle || !utf8.ValidString(reply.Question) {
			return nil
		}
		total += len(reply.Answer)
		if total > 16384 {
			return nil
		}
		seen[reply.QuestionItemID] = true
		answers = append(answers, agentproto.AsyncQuestionAnswer{QuestionItemID: reply.QuestionItemID, Answer: reply.Answer})
	}
	return answers
}
