package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/atomicfile"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
)

// A write-ahead receipt links the original human command to the bot-owned root.
// Empty RootID means the send may have happened: never replay it after a crash or
// timeout. A fresh command is the explicit recovery path for an unknown result.
type namedTopicCreation struct {
	Version   int    `json:"version"`
	GatewayID string `json:"gatewayID"`
	ChatID    string `json:"chatID"`
	CommandID string `json:"commandID"`
	ActorID   string `json:"actorID"`
	Title     string `json:"title"`
	RootID    string `json:"rootID,omitempty"`
}

func (a *App) prepareNamedTopicLocked(ctx context.Context, action control.Action) (control.Action, error) {
	if action.Kind != control.ActionTopicCommand {
		return action, nil
	}
	argument := strings.TrimSpace(action.Value)
	if argument == "" {
		_, argument, _ = strings.Cut(strings.TrimSpace(action.Text), " ")
	}
	command, title, _ := strings.Cut(argument, " ")
	ref, ok := feishuidentity.ParseSurfaceRef(action.SurfaceSessionID)
	if !strings.EqualFold(command, "new") || !ok || !ref.IsChat() || action.IsCardAction() || action.MessageID == "" || action.ActorUserID == "" {
		return action, nil // Keep existing command/owner/lifecycle validation.
	}
	if ref.GatewayID != action.GatewayID || ref.ScopeID != action.ChatID || action.FeishuTopicID != "" || action.FeishuTopicRootMessageID != "" {
		return action, fmt.Errorf("请在群主界面发送一条新的 @机器人 /topic new 名称，不要在已有话题或引用消息中创建")
	}
	if a.resolveTopicSurface(action.GatewayID, action.ChatID, "", action.MessageID) != "" {
		return action, nil // Old command-root topics retain their exact route.
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "新会话"
	}
	if utf8.RuneCountInString(title) > 200 {
		return action, fmt.Errorf("话题名称请控制在 200 字以内")
	}
	stateDir := strings.TrimSpace(a.headlessRuntime.Paths.StateDir)
	if stateDir == "" || a.gateway == nil || a.surfaceResumeRuntime.store == nil || !a.surfaceResumeRuntime.writable() {
		return action, fmt.Errorf("话题持久化或飞书入口尚未就绪，未创建话题")
	}
	if err := a.surfaceResumeRuntime.store.Save(); err != nil {
		return action, fmt.Errorf("话题状态无法保存，未发送标题消息：%w", err)
	}
	intent := namedTopicCreation{Version: 1, GatewayID: action.GatewayID, ChatID: action.ChatID, CommandID: action.MessageID, ActorID: action.ActorUserID, Title: title}
	digest := sha256.Sum256([]byte(intent.GatewayID + "\x00" + intent.ChatID + "\x00" + intent.CommandID))
	key := hex.EncodeToString(digest[:])
	path := filepath.Join(stateDir, "feishu-topic-creations", key+".json")
	raw, err := os.ReadFile(path)
	if err == nil {
		var saved namedTopicCreation
		if json.Unmarshal(raw, &saved) != nil || saved.Version != 1 || saved.GatewayID != intent.GatewayID || saved.ChatID != intent.ChatID ||
			saved.CommandID != intent.CommandID || saved.ActorID != intent.ActorID || saved.Title != intent.Title {
			return action, fmt.Errorf("话题创建记录不匹配，未重新发送；请发送一条新的创建命令")
		}
		if saved.RootID == "" {
			return action, fmt.Errorf("本条话题创建正在处理或发送结果未知，未重复创建；请先查看群里的标题消息，必要时发送一条新的创建命令")
		}
		intent = saved
	} else if !os.IsNotExist(err) {
		return action, fmt.Errorf("读取话题创建记录失败：%w", err)
	} else {
		if err := saveNamedTopicCreation(path, intent); err != nil {
			return action, err
		}
		ops := []feishu.Operation{{Kind: feishu.OperationCreateTopicRoot, GatewayID: intent.GatewayID, SurfaceSessionID: action.SurfaceSessionID,
			ChatID: intent.ChatID, Text: intent.Title, IdempotencyKey: key[:32]}}
		applyCtx, cancel := a.newTimeoutContext(ctx, a.gatewayApplyTimeout)
		a.mu.Unlock()
		err := a.gateway.Apply(applyCtx, ops)
		a.mu.Lock()
		cancel()
		if err != nil {
			return action, fmt.Errorf("标题消息发送失败或结果未知，未创建会话绑定；请先查看群消息再决定是否新建：%w", err)
		}
		if strings.TrimSpace(ops[0].MessageID) == "" {
			return action, fmt.Errorf("标题消息缺少发送回执，未创建会话绑定；请发送新的创建命令")
		}
		intent.RootID = strings.TrimSpace(ops[0].MessageID)
		if err := saveNamedTopicCreation(path, intent); err != nil {
			return action, err
		}
	}
	if a.shuttingDown {
		return action, fmt.Errorf("服务正在停止，标题回执已保存但绑定尚未完成；恢复后请重新创建话题")
	}
	// Core sees the actual immutable root while the inbound metadata and original
	// log retain the human command. The owner remains the human, never the bot.
	action.MessageID = intent.RootID
	action.Value = "new " + intent.Title
	return action, nil
}

func saveNamedTopicCreation(path string, intent namedTopicCreation) error {
	raw, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicfile.Write(path, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("保存话题创建记录失败，未继续绑定：%w", err)
	}
	return nil
}
