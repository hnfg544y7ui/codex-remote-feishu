package orchestrator

import (
	"regexp"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

const asyncCredentialPattern = `(?:密码|口令|私钥|密钥|\b(?:password|passphrase|private[ _-]?key|api[ _-]?key|access[ _-]?token|secret)\b)`

var (
	asyncCredentialMention      = regexp.MustCompile(`(?i)` + asyncCredentialPattern)
	asyncCredentialRequest      = regexp.MustCompile(`(?i)(?:请|麻烦|需要你|需要您|能否|你能|你可以|完成后|确认后|授权后|解锁后|然后|接着|please|could you|can you|need you to|after).{0,48}(?:输入|填写|提供|发送|发来|粘贴|告诉我|给出|告知|输入框|input|enter|provide|send|paste|supply|tell me).{0,80}` + asyncCredentialPattern + `|^(?:输入|填写|提供|发送|发来|粘贴|input\b|enter\b|provide\b|send\b|paste\b|supply\b).{0,80}` + asyncCredentialPattern)
	asyncCredentialValue        = regexp.MustCompile(`(?i)` + asyncCredentialPattern + `.{0,16}(?:是多少|是什么|发给我|告诉我|填在|填入|粘贴|的内容|的值)|(?:what is|what's).{0,40}` + asyncCredentialPattern)
	asyncCredentialNegative     = regexp.MustCompile(`(?i)(?:不要|无需|不必|勿|不需要|禁止|不能|do not|don't|never|no need to|not required to)(?:\s|再|直接|额外|另行|向我|给我|在这里|在此处|在群里|在群中|在聊天中|在本卡片){0,6}(?:输入|填写|提供|发送|发来|粘贴|告诉我|给出|告知|input|enter|provide|send|paste|supply|tell me).{0,80}?` + asyncCredentialPattern + `(?:\s*(?:或|和|、|/|or|and)\s*` + asyncCredentialPattern + `)*`)
	asyncCredentialExternal     = regexp.MustCompile(`(?i)在(?:你(?:的)?|您(?:的)?)?(?:本机|本地|系统(?:的)?窗口|系统弹窗|授权窗口|终端|受控(?:的)?(?:界面|渠道)|钥匙串|凭据管理器)|in (?:your |the )?(?:local |system )?(?:terminal|keychain|credential manager|authorization dialog)`)
	asyncCredentialChatSink     = regexp.MustCompile(`(?i)这里|此处|本卡片|回答框|回答中|回复中|本表单|此输入框|聊天|群里|群中|发给我|回复给我|粘贴|发来|paste|answer (?:box|field)|response (?:box|field)|send.{0,16}(?:me|here)|in (?:this|the) (?:chat|card)`)
	asyncCredentialConfirmation = regexp.MustCompile(`(?i)是否|确认|已(?:经|在|完成|配置|授权|解锁)|完成后|继续|确认后|have you|already|completed|continue|confirm`)
	asyncCredentialWorkflow     = regexp.MustCompile(`(?i)授权|解锁|签名|配置完成|已配置|已输入|受控|系统窗口|系统弹窗|authorization|authorized|unlock|signing|configured`)
	asyncCredentialFailure      = regexp.MustCompile(`(?i)拒绝签名|签名失败|认证失败|登录失败|authentication failed|signing refused|agent refused`)
	asyncCredentialMethod       = regexp.MustCompile(`(?i)^(?:使用|继续使用)?(?:密码|密钥)(?:登录|认证)[。？?\s]*$|(?:选择|哪种|哪个|which|choose).{0,32}(?:登录|认证|连接方式|authentication|login)`)
	asyncCredentialLabel        = regexp.MustCompile(`(?i)^(?:(?:sudo|ssh|pi|root|account|user|your|the|账户|账号|登录|本机|管理员|的)[\s:]*)*` + asyncCredentialPattern + `[\s:：?？]*$`)
)

// Only displayed questions and affirmative collection instructions participate.
// IDs are protocol correlation, not user input. Context such as a failed agent
// signature or a reminder not to post credentials must not seal a normal form.
func asyncQuestionNeedsControlledAuth(text string, questions []agentproto.AsyncQuestion) bool {
	if requestsAsyncCredentialValue(text) || asyncCredentialLabel.MatchString(strings.TrimSpace(text)) {
		return true
	}
	for _, question := range questions {
		if asyncCredentialQuestionSensitive(question.Title) {
			return true
		}
		for _, option := range question.Options {
			if asyncCredentialQuestionSensitive(option) {
				return true
			}
		}
	}
	return false
}

func asyncCredentialQuestionSensitive(text string) bool {
	if requestsAsyncCredentialValue(text) {
		return true
	}
	for _, clause := range asyncCredentialClauses(text) {
		clause = asyncCredentialNegative.ReplaceAllString(clause, "")
		if !asyncCredentialMention.MatchString(clause) {
			continue
		}
		if asyncCredentialMethod.MatchString(clause) {
			continue
		}
		if asyncCredentialFailure.MatchString(clause) {
			continue
		}
		if asyncCredentialConfirmation.MatchString(clause) && asyncCredentialWorkflow.MatchString(clause) {
			continue
		}
		// A workflow in one question/clause cannot authorize collecting the
		// credential-labelled field in another.
		return true
	}
	return false
}

func requestsAsyncCredentialValue(text string) bool {
	for _, clause := range asyncCredentialClauses(text) {
		// Erase just the negated instruction, not its entire clause. A second
		// affirmative request without punctuation must still be detected.
		clause = asyncCredentialNegative.ReplaceAllString(clause, "")
		if !asyncCredentialMention.MatchString(clause) {
			continue
		}
		if !asyncCredentialRequest.MatchString(strings.TrimSpace(clause)) && !asyncCredentialValue.MatchString(clause) {
			continue
		}
		if asyncCredentialExternal.MatchString(clause) && !asyncCredentialChatSink.MatchString(clause) {
			continue
		}
		return true
	}
	return false
}

func asyncCredentialClauses(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return strings.ContainsRune("\n。！？；，,;!?", r)
	})
}
