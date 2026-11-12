package orchestrator

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

// These records are deliberately ephemeral. Daemon freshness makes old forms
// unusable after restart; they never become native pending requests.
type asyncQuestionRecord struct {
	ID, SourceKey, Fingerprint, SurfaceID, ActorID, InstanceID string
	ThreadID, TurnID, ItemID, MessageID, ReplyToMessageID      string
	Questions                                                  []agentproto.AsyncQuestion
	SubmittedAnswers                                           []asyncQuestionAnswer
	Text, Phase, Status, Answer, CommandID, QueueID            string
	Revision                                                   int
	CreatedAt, ExpiresAt, Deadline                             time.Time
}

type asyncQuestionAnswer struct {
	QuestionItemID string `json:"questionItemId"`
	Question       string `json:"question"`
	Answer         string `json:"answer"`
}

func (s *Service) asyncQuestionContext(q *asyncQuestionRecord) bool {
	surface, inst := s.root.Surfaces[q.SurfaceID], s.root.Instances[q.InstanceID]
	return surface != nil && inst != nil && inst.Online && surface.AttachedInstanceID == q.InstanceID &&
		surface.SelectedThreadID == q.ThreadID && !surface.FeishuTopicClosed &&
		s.surfaceBackend(surface) == agentproto.BackendCodex && ordinaryThreadVisible(inst.Threads[q.ThreadID]) && s.surfaceOwnsThread(surface, q.ThreadID)
}

func boundedAsyncText(text string, max int) string {
	if len(text) <= max {
		return text
	}
	for max > 0 && !utf8.RuneStart(text[max]) {
		max--
	}
	return text[:max] + "…"
}

func (s *Service) createAsyncQuestionCard(instanceID string, event agentproto.Event, text string) ([]eventcontract.Event, bool) {
	if len(event.AsyncQuestions) == 0 || len(event.AsyncQuestions) > 8 || event.ThreadID == "" || event.TurnID == "" || event.ItemID == "" {
		return nil, false
	}
	surface := s.turnSurface(instanceID, event.ThreadID, event.TurnID)
	if surface == nil {
		return nil, false
	}
	actor := surface.FeishuTopicOwnerID
	if b := s.lookupRemoteTurn(instanceID, event.ThreadID, event.TurnID); b != nil {
		if item := surface.QueueItems[b.QueueItemID]; item != nil {
			actor = item.ActorUserID
		}
	}
	if actor == "" {
		actor = surface.ActorUserID
	}
	if actor == "" {
		return nil, false
	}
	questions := make([]agentproto.AsyncQuestion, len(event.AsyncQuestions))
	budget := 0
	seen := map[string]bool{}
	for i, question := range event.AsyncQuestions {
		if question.ID == "" || len(question.ID) > 512 || seen[question.ID] || strings.TrimSpace(question.Title) == "" || len(question.Title) > 4096 || len(question.Options) > 20 {
			return nil, false
		}
		seen[question.ID] = true
		budget += len(question.Title)
		for _, option := range question.Options {
			if strings.TrimSpace(option) == "" || len(option) > 1024 {
				return nil, false
			}
			budget += len(option)
		}
		questions[i] = question
		questions[i].Options = append([]string(nil), question.Options...)
	}
	if budget > 8192 {
		return nil, false
	}
	key := strings.Join([]string{surface.SurfaceSessionID, instanceID, event.ThreadID, event.TurnID, event.ItemID}, "\x00")
	encoded, _ := json.Marshal(questions)
	var replaced []eventcontract.Event
	for _, q := range s.turns.asyncQuestions {
		if q.SourceKey != key {
			continue
		}
		if q.Fingerprint == string(encoded) {
			return nil, true
		}
		// A changed question never inherits answers or an in-flight submission.
		if q.Phase == "checking" || q.Phase == "submitting" || q.Phase == "queued" {
			return nil, true
		}
		q.Phase, q.Answer, q.Status = "expired", "", "问题已更新，请使用新卡片。"
		q.Revision++
		if q.MessageID != "" {
			replaced = append(replaced, s.asyncQuestionCard(q, false))
		}
	}
	if len(s.turns.asyncQuestions) >= 256 {
		return nil, false
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, false
	}
	q := &asyncQuestionRecord{ID: "async-q-" + hex.EncodeToString(nonce), SourceKey: key, Fingerprint: string(encoded),
		SurfaceID: surface.SurfaceSessionID, ActorID: actor, InstanceID: instanceID, ThreadID: event.ThreadID, TurnID: event.TurnID, ItemID: event.ItemID,
		Questions: questions, Text: boundedAsyncText(text, 3000), Phase: "pending", Revision: 1, CreatedAt: s.now(), ExpiresAt: s.now().Add(30 * time.Minute)}
	if !s.asyncQuestionContext(q) {
		return nil, false
	}
	q.ReplyToMessageID, _ = s.replyAnchorForTurn(instanceID, event.ThreadID, event.TurnID)
	if asyncQuestionNeedsControlledAuth(text, questions) {
		q.Phase, q.Status = "sensitive", "此问题涉及凭据，不在群卡片收集。请通过受控登录或授权渠道处理，再回到原会话确认；不要在群里发送密码或密钥。"
		q.Text = "需要通过受控渠道补充授权或凭据。"
	}
	s.turns.asyncQuestions[q.ID] = q
	return append(replaced, s.asyncQuestionCard(q, true)), true
}

func (s *Service) asyncQuestionCard(q *asyncQuestionRecord, attention bool) eventcontract.Event {
	editable := q.Phase == "pending" || q.Phase == "deferred" || q.Phase == "continue"
	view := &control.FeishuPageView{PageID: "async-question", Title: "需要你回答", MessageID: q.MessageID,
		TrackingKey: q.ID, ThemeKey: "system", Patchable: true, Interactive: editable, Sealed: !editable,
		SuppressDefaultRelatedButtons: true}
	body := q.Text
	if (editable && q.Phase != "continue") || len(q.SubmittedAnswers) > 0 {
		body = asyncQuestionIntro(q.Text, q.Questions)
	}
	if strings.TrimSpace(body) != "" {
		view.BodySections = []control.FeishuCardTextSection{{Lines: []string{body}}}
	}
	if len(q.SubmittedAnswers) > 0 {
		view.AsyncQuestionReceiptSections = asyncQuestionAnswerSections(q.SubmittedAnswers, editable && q.Phase != "continue")
		switch q.Phase {
		case "checking", "submitting":
			view.Title = "回答提交中"
		case "continue":
			view.Title = "等待继续确认"
		case "queued":
			view.Title = "回答已排队"
		case "answered":
			view.Title = "回答已接收"
		case "unknown":
			view.Title = "回答状态待确认"
		case "expired":
			view.Title = "提问已失效"
		}
	}
	if q.Status != "" {
		view.NoticeSections = []control.FeishuCardTextSection{{Lines: []string{q.Status}}}
	}
	if editable {
		view.AsyncQuestion = &control.AsyncQuestionCardView{ID: q.ID, Revision: q.Revision, CanSubmit: q.Phase != "continue", CanContinue: q.Phase == "continue"}
		for _, question := range q.Questions {
			view.AsyncQuestion.Questions = append(view.AsyncQuestion.Questions, control.AsyncQuestionCardQuestion{ID: question.ID, Title: question.Title, Options: append([]string(nil), question.Options...)})
		}
	}
	e := eventcontract.NewEventFromPayload(eventcontract.PagePayload{View: *view}, eventcontract.EventMeta{
		Target: eventcontract.TargetRef{SurfaceSessionID: q.SurfaceID}, SourceMessageID: q.ReplyToMessageID,
	})
	e.Meta.Semantics.VisibilityClass = eventcontract.VisibilityClassAlwaysVisible
	e.Meta.MessageDelivery = eventcontract.ReplyThreadAppendOnlyDelivery()
	if q.MessageID != "" {
		e.Meta.MessageDelivery.Mutation = eventcontract.MessageMutationPatchSameMessage
	}
	if attention {
		e.Meta.Attention = eventcontract.AttentionAnnotation{MentionUserID: q.ActorID, Text: "需要你补充回答。"}
	}
	return e
}

// Remove only exact question/option lines already rendered by the form, keeping
// explanatory context intact. This does not infer questions from free text.
func asyncQuestionIntro(text string, questions []agentproto.AsyncQuestion) string {
	lineKey := func(line string) string {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "-*#> \t")
		if prefix, rest, ok := strings.Cut(line, ". "); ok && prefix != "" && strings.Trim(prefix, "0123456789") == "" {
			line = rest
		}
		return strings.TrimSpace(strings.Trim(line, "`*_"))
	}
	duplicates := map[string]bool{}
	for _, question := range questions {
		duplicates[lineKey(question.Title)] = true
		for _, option := range question.Options {
			duplicates[lineKey(option)] = true
		}
	}
	lines := strings.Split(text, "\n")
	intro := make([]string, 0, len(lines))
	for _, line := range lines {
		if !duplicates[lineKey(line)] {
			intro = append(intro, line)
		}
	}
	return strings.TrimSpace(strings.Join(intro, "\n"))
}

func (s *Service) handleAsyncQuestionAction(surface *state.SurfaceConsoleRecord, action control.Action) []eventcontract.Event {
	q := s.turns.asyncQuestions[action.AsyncQuestionID]
	if q == nil || q.SurfaceID != surface.SurfaceSessionID || q.ActorID != action.ActorUserID || q.MessageID == "" || q.MessageID != action.MessageID ||
		q.Revision != action.AsyncQuestionRevision || action.Inbound == nil || !action.Inbound.CardCallback || action.Inbound.CardDaemonLifecycleID == "" {
		return notice(surface, "async_question_stale", "提问卡片已失效、已处理或不属于你；请查看原会话的最新提问。")
	}
	if !s.asyncQuestionContext(q) || !s.now().Before(q.ExpiresAt) {
		return s.finishAsyncQuestion(q, "expired", "会话已切换、离线或卡片已过期，本次未发送回答。")
	}
	if q.Phase != "pending" && q.Phase != "deferred" && q.Phase != "continue" {
		return notice(surface, "async_question_busy", "该回答已提交或正在处理，请勿重复提交。")
	}
	if action.Kind == control.ActionAsyncQuestionDefer {
		if q.Phase != "continue" {
			q.Phase = "deferred"
		}
		q.Revision++
		q.Status = "稍后处理；原任务不受影响。卡片在首次提问后 30 分钟过期。"
		return []eventcontract.Event{s.asyncQuestionCard(q, false)}
	}
	if action.Kind == control.ActionAsyncQuestionContinue {
		if q.Phase != "continue" || q.Answer == "" {
			return notice(surface, "async_question_stale", "尚未确认需要另起一轮，请先提交回答。")
		}
		return s.continueAsyncQuestion(surface, q)
	}
	if q.Phase == "continue" {
		return notice(surface, "async_question_stale", "原轮次已结束，请点击“在原会话继续”。")
	}
	answer, submitted, err := formatAsyncQuestionAnswer(q.Questions, action.AsyncQuestionAnswers)
	if err != nil {
		return notice(surface, "async_question_invalid", err.Error())
	}
	q.Answer, q.Phase = answer, "checking"
	q.SubmittedAnswers = submitted
	q.Revision++
	q.Status = "正在只读确认原轮次；回答尚未发送。"
	q.CommandID, q.Deadline = "relay-async-question-probe-"+s.nextAgentCommandID(), s.now().Add(30*time.Second)
	return []eventcontract.Event{s.asyncQuestionCard(q, false), {Kind: eventcontract.KindAgentCommand, SurfaceSessionID: q.SurfaceID,
		Command: &agentproto.Command{CommandID: q.CommandID, Kind: agentproto.CommandThreadHistoryRead, Target: agentproto.Target{ThreadID: q.ThreadID}, HistoryRead: &agentproto.ThreadHistoryReadOptions{LatestTurnOnly: true}}}}
}

func formatAsyncQuestionAnswer(questions []agentproto.AsyncQuestion, values map[string]string) (string, []asyncQuestionAnswer, error) {
	result := make([]asyncQuestionAnswer, 0, len(questions))
	allowed := map[string]bool{}
	total := 0
	for i, question := range questions {
		textKey, choiceKey := fmt.Sprintf("answer_%d", i), fmt.Sprintf("choice_%d", i)
		allowed[textKey] = true
		text := strings.TrimSpace(values[textKey])
		if len(question.Options) > 0 {
			allowed[choiceKey] = true
			if choice := values[choiceKey]; choice != "" {
				selected := ""
				for index, option := range question.Options {
					if fmt.Sprint(index) == choice {
						selected = option
						break
					}
				}
				if selected == "" {
					return "", nil, fmt.Errorf("选项无效，请重新选择。")
				}
				if text != "" {
					text = selected + "\n补充：" + text
				} else {
					text = selected
				}
			}
		}
		if text == "" || len(text) > 5120 || len(values[textKey]) > 4096 || !utf8.ValidString(text) {
			return "", nil, fmt.Errorf("请回答每个问题；每个文本输入不超过 4096 字节。")
		}
		total += len(text)
		if total > 16384 {
			return "", nil, fmt.Errorf("回答过长，请缩短后提交。")
		}
		result = append(result, asyncQuestionAnswer{question.ID, question.Title, text})
	}
	for key := range values {
		if !allowed[key] {
			return "", nil, fmt.Errorf("表单字段不匹配，请使用最新提问卡片。")
		}
	}
	data, err := json.Marshal(result)
	if err != nil {
		return "", nil, err
	}
	return "<send_user_message_question_reply>\n" + string(data) + "\n</send_user_message_question_reply>", result, nil
}
