package feishu

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const externalCardImageURL = "https://preview.example.com/g/preview-id/03643b5e443e44f1?t=example-token&inline=1"

func TestExternalCardMarkdownImagesAsLinks(t *testing.T) {
	image := "![预览](" + externalCardImageURL + ")"
	link := "[预览](" + externalCardImageURL + ")"
	tests := []struct{ name, input, want string }{
		{"reported preview URL", "输出\n" + image + "\n完成", "输出\n" + link + "\n完成"},
		{"http and uppercase scheme", "![A](HTTP://example.com/a.png)", "[A](HTTP://example.com/a.png)"},
		{"uploaded key", "![A](img_v3_uploaded_key)", "![A](img_v3_uploaded_key)"},
		{"ordinary link", link, link},
		{"empty alt", "![](" + externalCardImageURL + ")", "[查看图片](" + externalCardImageURL + ")"},
		{"space alt", "![  ](https://example.com/a)", "[查看图片](https://example.com/a)"},
		{"nested label", "![图 [一]](https://example.com/a)", "[图 [一]](https://example.com/a)"},
		{"escaped label", `![图 \] 一](https://example.com/a)`, `[图 \] 一](https://example.com/a)`},
		{"balanced destination", "![图](https://example.com/a_(b(c)).png)", "[图](https://example.com/a_(b(c)).png)"},
		{"escaped destination", `![图](https://example.com/a\).png)`, `[图](https://example.com/a\).png)`},
		{"angle destination and title", `![图](<https://example.com/a.png> "图标题")`, `[图](<https://example.com/a.png> "图标题")`},
		{"destination whitespace", `![图](  <https://example.com/a.png>  )`, `[图](  <https://example.com/a.png>  )`},
		{"backtick fence", "```md\n" + image + "\n```\n" + image, "```md\n" + image + "\n```\n" + link},
		{"tilde fence", "~~~md\n" + image + "\n~~~", "~~~md\n" + image + "\n~~~"},
		{"inline code", "`" + image + "` " + image, "`" + image + "` " + link},
		{"long inline code", "``a ` " + image + "``` b`` " + image, "``a ` " + image + "``` b`` " + link},
		{"unclosed backtick", "` " + image, "` " + link},
		{"escaped image", `\` + image, `\` + image},
		{"escaped exclamation", `!\[图](https://example.com/a)`, `!\[图](https://example.com/a)`},
		{"escaped backslash before image", `\\` + image, `\\` + link},
		{"invalid image", "![图](https://example.com/a", "![图](https://example.com/a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := externalMarkdownImagesAsLinks(tt.input); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderCardMarkdownImagesNestedAndImmutable(t *testing.T) {
	image := "![预览](" + externalCardImageURL + ")"
	link := "[预览](" + externalCardImageURL + ")"
	raw := map[string]any{
		"tag": "column_set",
		"columns": []any{map[string]any{
			"tag": "column",
			"elements": []map[string]any{
				{"tag": "markdown", "content": image},
				{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": image}},
				{"tag": "plain_text", "content": image},
				{"tag": "button", "value": map[string]any{"tag": "markdown", "content": image}},
				{"tag": "button", "behaviors": []any{map[string]any{"type": "callback", "value": map[string]any{"tag": "lark_md", "content": image}}}},
			},
		}},
	}
	doc := newCardDocumentWithHeader("图片", cardTextTagPlainText, image, cardTextTagLarkMarkdown, cardThemeInfo,
		cardMarkdownComponent{Content: image}, newRawCardComponent(raw))
	before, _ := json.Marshal(doc)
	rawBefore, _ := json.Marshal(raw)
	componentBefore, _ := json.Marshal(doc.Components[1].(cardRawComponent).data)
	first := renderCardDocument(doc, cardEnvelopeV2, false)
	second := renderCardDocument(doc, cardEnvelopeV2, false)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("rendering the same source must be repeatable")
	}
	after, _ := json.Marshal(doc)
	rawAfter, _ := json.Marshal(raw)
	componentAfter, _ := json.Marshal(doc.Components[1].(cardRawComponent).data)
	if string(before) != string(after) || string(rawBefore) != string(rawAfter) || string(componentBefore) != string(componentAfter) {
		t.Fatal("rendering mutated the document or caller's raw elements")
	}
	header := first["header"].(map[string]any)
	if header["subtitle"].(map[string]any)["content"] != link {
		t.Fatal("lark_md header did not normalize its external image")
	}
	elements := first["body"].(map[string]any)["elements"].([]map[string]any)
	if elements[0]["content"] != link {
		t.Fatal("ordinary body Markdown did not normalize its external image")
	}
	nested := elements[1]["columns"].([]any)[0].(map[string]any)["elements"].([]map[string]any)
	if nested[0]["content"] != link || nested[1]["text"].(map[string]any)["content"] != link {
		t.Fatal("nested Markdown or lark_md was not normalized")
	}
	if nested[2]["content"] != image || nested[3]["value"].(map[string]any)["content"] != image {
		t.Fatal("plain text or callback data changed")
	}
	value := nested[4]["behaviors"].([]any)[0].(map[string]any)["value"].(map[string]any)
	if value["content"] != image {
		t.Fatal("native callback behavior data changed")
	}
}

func TestGatewayCardTransportExternalMarkdownImages(t *testing.T) {
	for _, mode := range []string{"create", "reply", "patch"} {
		t.Run(mode, func(t *testing.T) {
			gateway := NewLiveGateway(LiveGatewayConfig{GatewayID: "app-1"})
			var content string
			gateway.createMessageFn = func(_ context.Context, _, _, _ string, card string) (*larkim.CreateMessageResp, error) {
				content = card
				return &larkim.CreateMessageResp{}, nil
			}
			gateway.replyMessageFn = func(_ context.Context, _, _ string, card string) (*larkim.ReplyMessageResp, error) {
				content = card
				return &larkim.ReplyMessageResp{}, nil
			}
			gateway.patchMessageFn = func(_ context.Context, _ string, card string) (*larkim.PatchMessageResp, error) {
				content = card
				return &larkim.PatchMessageResp{}, nil
			}
			op := Operation{
				Kind: OperationSendCard, SurfaceSessionID: "surface-1", ChatID: "oc_1",
				CardBody: "![预览](" + externalCardImageURL + ")\n![上传](img_v3_valid)",
			}
			if mode == "reply" {
				op.ReplyToMessageID = "om-source"
			} else if mode == "patch" {
				op.Kind, op.MessageID = OperationUpdateCard, "om-card"
			}
			if err := gateway.Apply(t.Context(), []Operation{op}); err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(content), &payload); err != nil {
				t.Fatal(err)
			}
			body := payload["body"].(map[string]any)["elements"].([]any)[0].(map[string]any)["content"].(string)
			if body != "[预览]("+externalCardImageURL+")\n![上传](img_v3_valid)" {
				t.Fatalf("unsafe or damaged %s payload: %q", mode, body)
			}
			if !strings.HasPrefix(op.CardBody, "![预览]") {
				t.Fatal("transport changed original operation")
			}
		})
	}
}
