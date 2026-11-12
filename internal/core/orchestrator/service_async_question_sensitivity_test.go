package orchestrator

import (
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

func TestAsyncQuestionNeedsControlledAuthClassifiesActualCollection(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		questions []agentproto.AsyncQuestion
		blocked   bool
	}{
		{name: "opaque-secret-id", questions: []agentproto.AsyncQuestion{{ID: "secret_setup_done", Title: "是否继续检查？", Options: []string{"继续", "稍后"}}}},
		{name: "opaque-password-id", questions: []agentproto.AsyncQuestion{{ID: "password-status-confirmation", Title: "是否继续检查？", Options: []string{"继续", "稍后"}}}},
		{name: "ssh-agent-local-authorization", text: "无需提供密码或私钥。GitLab已接受本机公钥，但SSH agent拒绝签名，请在本机确认授权", questions: []agentproto.AsyncQuestion{{ID: "ssh-agent-confirm", Title: "是否已在本机确认允许签名？", Options: []string{"已完成授权，请重试", "稍后处理"}}}},
		{name: "key-authorization-status", questions: []agentproto.AsyncQuestion{{ID: "key-status", Title: "密钥授权是否已完成？", Options: []string{"已完成", "尚未完成"}}}},
		{name: "password-already-configured", questions: []agentproto.AsyncQuestion{{ID: "password-status", Title: "密码已在受控界面配置完成，是否继续？", Options: []string{"继续", "稍后"}}}},
		{name: "authentication-method-choice", questions: []agentproto.AsyncQuestion{{ID: "auth-method", Title: "选择SSH认证方式", Options: []string{"密码登录", "密钥登录"}}}},
		{name: "passwordless-context", text: "Passwordless authentication is enabled.", questions: []agentproto.AsyncQuestion{{ID: "migration-plan", Title: "是否继续验证登录流程？", Options: []string{"继续", "稍后"}}}},
		{name: "actual-sudo-password", questions: []agentproto.AsyncQuestion{{ID: "sudo", Title: "请提供sudo密码"}}, blocked: true},
		{name: "noun-password-title", questions: []agentproto.AsyncQuestion{{ID: "pi-password", Title: "pi账户密码"}}, blocked: true},
		{name: "credential-request-only-in-context", text: "请提供sudo密码以完成后续步骤。", questions: []agentproto.AsyncQuestion{{ID: "supplement", Title: "补充信息"}}, blocked: true},
		{name: "mixed-question-batch", questions: []agentproto.AsyncQuestion{{ID: "continue", Title: "是否继续？", Options: []string{"是", "否"}}, {ID: "credential", Title: "请粘贴私钥内容"}}, blocked: true},
		{name: "affirmative-private-key-overrides-negative", text: "无需提供密码。请粘贴私钥内容", questions: []agentproto.AsyncQuestion{{ID: "supplement", Title: "补充信息"}}, blocked: true},
		{name: "affirmative-without-punctuation", text: "无需提供密码也请提供私钥", questions: []agentproto.AsyncQuestion{{ID: "supplement", Title: "补充信息"}}, blocked: true},
		{name: "local-terminal-unlock", text: "请在本机终端输入私钥口令完成授权，完成后只回复已完成。", questions: []agentproto.AsyncQuestion{{ID: "local-auth", Title: "是否已完成密钥授权？"}}},
		{name: "workflow-does-not-authorize-another-field", questions: []agentproto.AsyncQuestion{{ID: "supplement", Title: "密钥已授权，pi账户密码"}}, blocked: true},
		{name: "unrelated-negation-does-not-hide-request", text: "无需重启请提供密码", questions: []agentproto.AsyncQuestion{{ID: "supplement", Title: "补充信息"}}, blocked: true},
		{name: "local-workflow-does-not-hide-answer-box", text: "请在本机确认授权后在回答框填写密码", questions: []agentproto.AsyncQuestion{{ID: "supplement", Title: "补充信息"}}, blocked: true},
		{name: "collect-after-authorization", questions: []agentproto.AsyncQuestion{{ID: "supplement", Title: "授权完成后填写密码"}}, blocked: true},
		{name: "signing-failure-followed-by-confirmation", questions: []agentproto.AsyncQuestion{{ID: "sign-status", Title: "本机私钥拒绝签名，是否已允许本次签名？"}}},
		{name: "signing-failure-does-not-hide-collection", questions: []agentproto.AsyncQuestion{{ID: "sign-status", Title: "私钥拒绝签名，请提供私钥口令"}}, blocked: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := asyncQuestionNeedsControlledAuth(tc.text, tc.questions); got != tc.blocked {
				t.Fatalf("blocked=%v, want %v", got, tc.blocked)
			}
		})
	}
}

func TestAsyncQuestionSafeLocalAuthorizationUsesNormalAnswerLifecycle(t *testing.T) {
	s, surface, oldQuestion, _ := asyncCardFixture(t)
	active := surface.ActiveQueueItemID
	s.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: "surface", ChatID: "chat", ActorUserID: "owner", MessageID: "unrelated", Text: "另一个已排队任务"})
	other := surface.QueuedQueueItemIDs[0]
	source := asyncCardSource()
	source.ItemID = "ssh-agent-authorization-confirmation"
	source.Metadata["text"] = "无需提供密码或私钥。GitLab已接受本机公钥，但SSH agent拒绝签名，请在本机确认授权"
	source.AsyncQuestions = []agentproto.AsyncQuestion{{ID: "secret-signing-confirmation", Title: "是否已在本机确认允许签名？", Options: []string{"已完成授权，请重试", "稍后处理"}}}
	events := s.ApplyAgentEvent("local", source)
	var question *asyncQuestionRecord
	for _, event := range events {
		if event.Command != nil {
			t.Fatal("showing a safe confirmation executed backend work")
		}
		if event.PageView == nil {
			continue
		}
		if event.PageView.Sealed || event.PageView.AsyncQuestion == nil {
			t.Fatal("safe external-authorization confirmation was sealed")
		}
		question = s.turns.asyncQuestions[event.PageView.AsyncQuestion.ID]
		if event.Meta.Attention.MentionUserID != "owner" {
			t.Fatal("confirmation did not mention actual task owner")
		}
	}
	if question == nil || question == oldQuestion || question.Phase != "pending" || oldQuestion.Phase != "pending" {
		t.Fatal("fresh question altered an existing question")
	}
	s.RecordPageTrackingMessage("surface", question.ID, "ssh-confirm-card")
	submit := asyncCardAction(question, control.ActionAsyncQuestionSubmit)
	submit.AsyncQuestionAnswers = map[string]string{"choice_0": "0"}
	wrongActor := submit
	wrongActor.ActorUserID = "another-member"
	for _, event := range s.ApplySurfaceAction(wrongActor) {
		if event.Command != nil {
			t.Fatal("another member submitted authorization confirmation")
		}
	}
	if question.Phase != "pending" {
		t.Fatal("wrong actor changed question state")
	}
	probe := asyncCommand(t, s.ApplySurfaceAction(submit), agentproto.CommandThreadHistoryRead)
	if !probe.HistoryRead.LatestTurnOnly || question.Phase != "checking" {
		t.Fatal("confirmation skipped the original-turn probe")
	}
	if len(question.SubmittedAnswers) != 1 || question.SubmittedAnswers[0].Answer != "已完成授权，请重试" {
		t.Fatal("confirmation receipt lost selected status")
	}
	steer := asyncCommand(t, s.ApplyAgentEvent("local", asyncProbe(question, "turn")), agentproto.CommandTurnSteer)
	if steer.Target.ThreadID != "thread" || steer.Target.TurnID != "turn" || len(steer.Prompt.Inputs) != 1 {
		t.Fatal("confirmation was not directed to original turn")
	}
	wire := steer.Prompt.Inputs[0].Text
	if !strings.Contains(wire, `"questionItemId":"secret-signing-confirmation"`) || !strings.Contains(wire, "已完成授权，请重试") || strings.Contains(wire, "另一个已排队任务") {
		t.Fatal("confirmation changed correlation ID or merged unrelated work")
	}
	if question.Phase != "submitting" || surface.ActiveQueueItemID != active || len(surface.QueuedQueueItemIDs) != 1 || surface.QueuedQueueItemIDs[0] != other || len(surface.PendingRequests) != 0 || surface.ActiveRequestCapture != nil {
		t.Fatal("confirmation changed queue or native-request ownership")
	}
	acknowledged := s.HandleCommandAccepted("local", agentproto.CommandAck{CommandID: steer.CommandID, Accepted: true})
	if question.Phase != "answered" || question.Answer != "" || len(acknowledged) != 1 || acknowledged[0].PageView == nil || !acknowledged[0].PageView.Sealed {
		t.Fatal("confirmation was not sealed after backend ACK")
	}
	if !asyncSensitivityReceiptContains(acknowledged[0], "已完成授权，请重试") {
		t.Fatal("accepted confirmation lost visible answer receipt")
	}
	if len(s.HandleCommandAccepted("local", agentproto.CommandAck{CommandID: steer.CommandID, Accepted: true})) != 0 {
		t.Fatal("duplicate ACK repeated confirmation")
	}
}

func TestAsyncQuestionActualCredentialContextRemainsSealed(t *testing.T) {
	s, _, _, _ := asyncCardFixture(t)
	source := asyncCardSource()
	source.ItemID = "actual-private-key-request"
	source.Metadata["text"] = "无需提供密码。请粘贴私钥内容"
	source.AsyncQuestions = []agentproto.AsyncQuestion{{ID: "supplement", Title: "补充信息"}}
	events := s.ApplyAgentEvent("local", source)
	var question *asyncQuestionRecord
	for _, event := range events {
		if event.Command != nil {
			t.Fatal("credential prompt executed backend work")
		}
		if event.PageView == nil {
			continue
		}
		if !event.PageView.Sealed || event.PageView.AsyncQuestion != nil {
			t.Fatal("actual credential collection exposed a form")
		}
		question = s.turns.asyncQuestions[event.PageView.TrackingKey]
	}
	if question == nil || question.Phase != "sensitive" {
		t.Fatal("actual credential prompt was not kept sensitive")
	}
	s.RecordPageTrackingMessage("surface", question.ID, "credential-card")
	for _, event := range s.ApplySurfaceAction(asyncCardAction(question, control.ActionAsyncQuestionSubmit)) {
		if event.Command != nil {
			t.Fatal("forged sensitive-card submission executed")
		}
	}
	if question.Phase != "sensitive" || question.Answer != "" || len(question.SubmittedAnswers) != 0 {
		t.Fatal("sensitive record accepted a forged answer")
	}
}

func asyncSensitivityReceiptContains(event eventcontract.Event, text string) bool {
	if event.PageView == nil {
		return false
	}
	for _, section := range event.PageView.AsyncQuestionReceiptSections {
		for _, line := range section.Lines {
			if strings.Contains(line, text) {
				return true
			}
		}
	}
	return false
}
