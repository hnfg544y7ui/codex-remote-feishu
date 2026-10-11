package feishu

import (
	"encoding/json"
	"strings"
	"testing"

	projectorpkg "github.com/kxn/codex-remote-feishu/internal/adapter/feishu/projector"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/frontstagecontract"
	larkcallback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestDesktopProjectPickerCardUsesProjectIdentityWithoutChangingExecutionDirectory(t *testing.T) {
	elements := projectorpkg.TargetPickerElements(control.FeishuTargetPickerView{
		PickerID: "picker", Page: control.FeishuTargetPickerPageTarget,
		Stage: control.FeishuTargetPickerStageEditing, ProjectGrouped: true,
		WorkspaceSelectionValue: "desktop-project:p1", SelectedWorkspaceKey: "/actual/execution/directory",
		WorkspaceOptions:     []control.FeishuTargetPickerWorkspaceOption{{Value: "desktop-project:p1", Label: "臂环开发"}},
		SelectedSessionValue: "thread:t1", SessionOptions: []control.FeishuTargetPickerSessionOption{{Value: "thread:t1", Label: "测试会话", Kind: control.FeishuTargetPickerSessionThread}},
		CanConfirm: true,
	}, "life-projects")
	data, err := json.Marshal(elements)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "/actual/execution/directory") || !strings.Contains(string(data), "臂环开发") {
		t.Fatalf("project card must display the project, not a directory: %s", data)
	}
	found := false
	for _, action := range cardActionsFromElements(elements) {
		payload := cardValueMap(action)
		if payload[frontstagecontract.CardActionPayloadKeyDaemonLifecycleID] != "life-projects" {
			t.Fatalf("project action lost lifecycle protection: %#v", action)
		}
		if payload[frontstagecontract.CardActionPayloadKeyKind] == cardActionKindTargetPickerSelectWorkspace {
			found = true
			if action["initial_option"] != "desktop-project:p1" {
				t.Fatalf("project selection was initialized from execution CWD: %#v", action)
			}
		}
	}
	if !found {
		t.Fatal("missing project select callback")
	}
}

func TestDesktopProjectPickerCallbackKeepsNewestProjectAndSessionValues(t *testing.T) {
	for _, kind := range []string{cardActionKindTargetPickerSelectWorkspace, cardActionKindTargetPickerConfirm} {
		gateway := NewLiveGateway(LiveGatewayConfig{GatewayID: "app"})
		gateway.recordSurfaceMessage("card", "feishu:app:user:user")
		user := "user"
		action, ok := gateway.parseCardActionTriggerEvent(&larkcallback.CardActionTriggerEvent{
			Event: &larkcallback.CardActionTriggerRequest{
				Operator: &larkcallback.Operator{UserID: &user},
				Context:  &larkcallback.Context{OpenChatID: "chat", OpenMessageID: "card"},
				Action: &larkcallback.CallBackAction{
					Value:  map[string]any{"kind": kind, "picker_id": "picker"},
					Option: "desktop-project:old",
					FormValue: map[string]interface{}{
						cardTargetPickerWorkspaceFieldName: []interface{}{"desktop-project:new"},
						cardTargetPickerSessionFieldName:   []interface{}{"thread:t1"},
					},
				},
			},
		})
		if !ok || action.WorkspaceKey != "desktop-project:new" {
			t.Fatalf("project identity altered or stale option won: %#v", action)
		}
		if kind == cardActionKindTargetPickerConfirm && action.TargetPickerValue != "thread:t1" {
			t.Fatalf("confirm lost session: %#v", action)
		}
	}
}
