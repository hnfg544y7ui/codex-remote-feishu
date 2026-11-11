package codex

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

const (
	maxAsyncQuestions     = 8
	maxAsyncQuestionID    = 512
	maxAsyncQuestionTitle = 4096
	maxAsyncOptions       = 20
	maxAsyncOptionText    = 1024
)

// Retain no partial list: skipping a malformed question or option would change
// its source index and could attach an answer to a different question.
func extractAsyncQuestions(itemKind, itemID string, item map[string]any) []agentproto.AsyncQuestion {
	if itemKind != "agent_message" || item["delivery"] != "async" {
		return nil
	}
	raw, ok := item["questions"].([]any)
	if !ok || len(raw) == 0 || len(raw) > maxAsyncQuestions {
		return nil
	}
	questions := make([]agentproto.AsyncQuestion, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for index, value := range raw {
		question, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		title, ok := question["title"].(string)
		if !ok || !validAsyncQuestionText(title, maxAsyncQuestionTitle) {
			return nil
		}
		id, ok := asyncQuestionSourceID(question)
		if !ok {
			return nil
		}
		if id == "" {
			if strings.TrimSpace(itemID) == "" {
				return nil
			}
			id = "async:" + itemID + ":" + strconv.Itoa(index)
		}
		if !validAsyncQuestionText(id, maxAsyncQuestionID) || seen[id] {
			return nil
		}
		seen[id] = true
		normalized := agentproto.AsyncQuestion{ID: id, Title: strings.TrimSpace(title)}
		if question["options"] != nil {
			options, ok := question["options"].([]any)
			if !ok || len(options) > maxAsyncOptions {
				return nil
			}
			for _, value := range options {
				option, ok := value.(string)
				if !ok || !validAsyncQuestionText(option, maxAsyncOptionText) {
					return nil
				}
				normalized.Options = append(normalized.Options, strings.TrimSpace(option))
			}
		}
		questions = append(questions, normalized)
	}
	return questions
}

func asyncQuestionSourceID(question map[string]any) (string, bool) {
	var result string
	for _, name := range []string{"id", "questionItemId"} {
		if question[name] == nil {
			continue
		}
		id, ok := question[name].(string)
		if !ok {
			return "", false
		}
		if strings.TrimSpace(id) == "" {
			continue
		}
		if result != "" && result != id {
			return "", false
		}
		result = id
	}
	return result, true
}

func validAsyncQuestionText(value string, maxBytes int) bool {
	return len(value) <= maxBytes && strings.TrimSpace(value) != "" && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}
