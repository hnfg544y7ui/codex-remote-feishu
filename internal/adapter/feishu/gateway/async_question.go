package gateway

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/frontstagecontract"
	larkcallback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func isAsyncQuestionCardActionKind(kind string) bool {
	switch kind {
	case frontstagecontract.CardActionKindAsyncQuestionSubmit, frontstagecontract.CardActionKindAsyncQuestionContinue,
		frontstagecontract.CardActionKindAsyncQuestionDefer:
		return true
	}
	return false
}

func parseAsyncQuestionCardAction(event *larkcallback.CardActionTriggerEvent, action control.Action) (control.Action, bool) {
	value := event.Event.Action.Value
	id, ok := value[frontstagecontract.CardActionPayloadKeyAsyncQuestionID].(string)
	if !ok || strings.TrimSpace(id) == "" || len(id) > 512 || action.ActorUserID == "" || action.MessageID == "" {
		return control.Action{}, false
	}
	revision, ok := asyncQuestionRevision(value[frontstagecontract.CardActionPayloadKeyAsyncQuestionRevision])
	if !ok {
		return control.Action{}, false
	}
	action.AsyncQuestionID, action.AsyncQuestionRevision = strings.TrimSpace(id), revision
	switch actionPayloadKind(value) {
	case frontstagecontract.CardActionKindAsyncQuestionSubmit:
		action.Kind = control.ActionAsyncQuestionSubmit
		answers, ok := asyncQuestionFormAnswers(event.Event.Action.FormValue)
		if !ok {
			return control.Action{}, false
		}
		action.AsyncQuestionAnswers = answers
	case frontstagecontract.CardActionKindAsyncQuestionContinue:
		if len(event.Event.Action.FormValue) != 0 {
			return control.Action{}, false
		}
		action.Kind = control.ActionAsyncQuestionContinue
	case frontstagecontract.CardActionKindAsyncQuestionDefer:
		if len(event.Event.Action.FormValue) != 0 {
			return control.Action{}, false
		}
		action.Kind = control.ActionAsyncQuestionDefer
	default:
		return control.Action{}, false
	}
	return action, true
}

func asyncQuestionRevision(value any) (int, bool) {
	var number int64
	switch value := value.(type) {
	case int:
		number = int64(value)
	case int64:
		number = value
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value < 1 || value > 1_000_000_000 {
			return 0, false
		}
		number = int64(value)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
		if err != nil {
			return 0, false
		}
		number = parsed
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		return 0, false
	}
	return int(number), number > 0 && number <= 1_000_000_000
}

func asyncQuestionFormAnswers(values map[string]any) (map[string]string, bool) {
	if len(values) > 16 {
		return nil, false
	}
	answers := make(map[string]string, len(values))
	total := 0
	for name, raw := range values {
		choice := strings.HasPrefix(name, "choice_")
		prefix := "answer_"
		if choice {
			prefix = "choice_"
		}
		if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+1 || name[len(prefix)] < '0' || name[len(prefix)] > '7' {
			return nil, false
		}
		if raw == nil {
			raw = ""
		}
		text, ok := raw.(string)
		if !ok || len(text) > 4096 || total+len(text) > 16384 {
			return nil, false
		}
		total += len(text)
		text = strings.TrimSpace(text)
		if choice && text != "" {
			number, err := strconv.Atoi(text)
			if err != nil || number < 0 || number > 19 || strconv.Itoa(number) != text {
				return nil, false
			}
		}
		answers[name] = text
	}
	return answers, true
}
