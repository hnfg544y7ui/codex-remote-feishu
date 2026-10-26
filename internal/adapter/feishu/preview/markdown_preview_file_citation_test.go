package preview

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/render"
)

func TestParseCodexOutputFileCitation(t *testing.T) {
	tests := []struct {
		name, text, path string
		valid            bool
	}{
		{"unix", `:codex-file-citation{path="/workspace/四目相机使用说明书.docx" purpose="output"}`, "/workspace/四目相机使用说明书.docx", true},
		{"windows JSON escapes", `:codex-file-citation{path="C:\\fixtures\\camera docs\\说明书.docx" purpose="output"}`, `C:\fixtures\camera docs\说明书.docx`, true},
		{"reordered with whitespace", ":codex-file-citation {\n purpose = \"output\" \n path = \"\n /workspace/camera docs/说明书.docx \n\"\n}", "/workspace/camera docs/说明书.docx", true},
		{"escaped whitespace", `:codex-file-citation{path="\n/workspace/doc.docx\n" purpose="output"}`, "/workspace/doc.docx", true},
		{"braces in path", `:codex-file-citation{path="/workspace/{camera}/doc.docx" purpose="output"}`, "/workspace/{camera}/doc.docx", true},
		{"unknown purpose", `:codex-file-citation{path="/workspace/doc.docx" purpose="input"}`, "", false},
		{"missing purpose", `:codex-file-citation{path="/workspace/doc.docx"}`, "", false},
		{"duplicate attr", `:codex-file-citation{path="/workspace/doc.docx" path="/tmp/no.docx" purpose="output"}`, "", false},
		{"unknown attr", `:codex-file-citation{path="/workspace/doc.docx" purpose="output" extra="x"}`, "", false},
		{"invalid JSON escape", `:codex-file-citation{path="C:\fixtures\doc.docx" purpose="output"}`, "", false},
		{"missing separator", `:codex-file-citation{path="/workspace/doc.docx"purpose="output"}`, "", false},
		{"unterminated", `:codex-file-citation{path="/workspace/doc.docx" purpose="output"`, "", false},
		{"relative path", `:codex-file-citation{path="docs/doc.docx" purpose="output"}`, "", false},
		{"external URL", `:codex-file-citation{path="https://example.com/doc.docx" purpose="output"}`, "", false},
		{"escaped annotation", `\:codex-file-citation{path="/workspace/doc.docx" purpose="output"}`, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := strings.Index(tt.text, codexFileCitationPrefix)
			citation, recognized := parseCodexOutputFileCitation(tt.text, start)
			if !recognized || citation.end != len(tt.text) || citation.valid != tt.valid {
				t.Fatalf("unexpected parse: %#v recognized=%v", citation, recognized)
			}
			if tt.valid && citation.path != tt.path {
				t.Fatalf("path=%q want=%q", citation.path, tt.path)
			}
		})
	}
}

func TestCodexOutputFileCitationPublishesWholeAnnotationOnce(t *testing.T) {
	root := t.TempDir()
	file := writePreviewFile(t, filepath.Join(root, "camera docs", "四目相机[说明书].docx"), "document bytes")
	api := newFakePreviewAPI()
	p := NewDriveMarkdownPreviewer(api, MarkdownPreviewConfig{ProcessCWD: root, CacheDir: filepath.Join(root, "cache")})
	web := &fakeWebPreviewPublisher{baseURL: "https://preview.example/g/shared/?t=example-token"}
	p.SetWebPreviewPublisher(web)
	probe := &citationReferenceProbe{}
	p.handlers = append([]FinalBlockPreviewHandler{probe}, p.handlers...)
	citation := outputFileCitation(file)
	text := "已完成：" + citation + "\n重复：" + citation
	req := citationPreviewRequest(root, text)
	result, err := p.RewriteFinalBlock(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Block.Text, codexFileCitationPrefix) || strings.Contains(result.Block.Text, `purpose=`) {
		t.Fatalf("annotation leaked: %q", result.Block.Text)
	}
	if strings.Count(result.Block.Text, `[四目相机\[说明书\].docx](https://preview.example/g/shared/`) != 2 {
		t.Fatalf("expected two basename links: %q", result.Block.Text)
	}
	scope := previewScopePublicID(previewScopeKey(req.GatewayID, req.SurfaceSessionID, req.ChatID, req.ActorUserID))
	if len(web.issuedFor) != 1 || web.issuedFor[0].ScopePublicID != scope || web.issuedFor[0].GrantKey == "" {
		t.Fatalf("unexpected scoped publication: %#v", web.issuedFor)
	}
	manifest, err := p.loadWebPreviewScopeManifest(scope)
	if err != nil || manifest == nil || len(manifest.Records) != 1 {
		t.Fatalf("expected one materialized file: %#v err=%v", manifest, err)
	}
	if len(api.uploadFileCalls) != 0 || len(probe.refs) != 1 {
		t.Fatal("citation bypassed publication mode or per-target deduplication")
	}
	ref := probe.refs[0]
	encoded, _ := json.Marshal(file)
	wantStart := strings.Index(text, string(encoded)) + 1
	if ref.RawTarget != file || ref.TargetStart != wantStart || ref.TargetEnd != wantStart+len(encoded)-2 {
		t.Fatalf("original source offsets lost: %#v wantStart=%d", ref, wantStart)
	}
	if req.Block.Text != text {
		t.Fatal("rewriting mutated request source")
	}
}

func TestCodexOutputFileCitationPreservesCodeMalformedAndUnknown(t *testing.T) {
	root := t.TempDir()
	file := writePreviewFile(t, filepath.Join(root, "doc.docx"), "document bytes")
	citation := outputFileCitation(file)
	q, _ := json.Marshal(file)
	tests := []string{
		"`" + citation + "`",
		"```text\n" + citation + "\n```",
		"~~~\n" + citation + "\n~~~",
		strings.Replace(citation, `purpose="output"`, `purpose="input"`, 1),
		strings.Replace(citation, `purpose="output"`, `purpose=output`, 1),
		strings.TrimSuffix(citation, "}"),
		codexFileCitationPrefix + `{path="unterminated ` + string(q) + ` purpose="output"}`,
		`\` + citation,
	}
	for i, text := range tests {
		p := NewDriveMarkdownPreviewer(newFakePreviewAPI(), MarkdownPreviewConfig{ProcessCWD: root, CacheDir: t.TempDir()})
		web := &fakeWebPreviewPublisher{baseURL: "https://preview.example/g/shared/?t=example-token"}
		p.SetWebPreviewPublisher(web)
		result, err := p.RewriteFinalBlock(t.Context(), citationPreviewRequest(root, text))
		if err != nil || result.Block.Text != text || len(web.issuedFor) != 0 {
			t.Fatalf("case %d annotation/code changed: %q err=%v grants=%d", i, result.Block.Text, err, len(web.issuedFor))
		}
	}
}

func TestCodexOutputFileCitationFailureRetainsWholeAnnotation(t *testing.T) {
	root := t.TempDir()
	file := writePreviewFile(t, filepath.Join(root, "doc.docx"), "document bytes")
	p := NewDriveMarkdownPreviewer(newFakePreviewAPI(), MarkdownPreviewConfig{ProcessCWD: root, CacheDir: t.TempDir()})
	web := &fakeWebPreviewPublisher{returnErr: errors.New("publication failed")}
	p.SetWebPreviewPublisher(web)
	text := "请下载 " + outputFileCitation(file)
	result, err := p.RewriteFinalBlock(t.Context(), citationPreviewRequest(root, text))
	if err == nil || !strings.Contains(err.Error(), "publication failed") || result.Block.Text != text {
		t.Fatalf("failure must retain complete source and report error: text=%q err=%v", result.Block.Text, err)
	}
}

func TestCodexOutputFileCitationMultiplePathsAndLiteralBackticks(t *testing.T) {
	root := t.TempDir()
	first := writePreviewFile(t, filepath.Join(root, "first.docx"), "first document")
	second := writePreviewFile(t, filepath.Join(root, "带`符号的说明书.docx"), "second document")
	p := NewDriveMarkdownPreviewer(newFakePreviewAPI(), MarkdownPreviewConfig{ProcessCWD: root, CacheDir: t.TempDir()})
	p.SetWebPreviewPublisher(&fakeWebPreviewPublisher{baseURL: "https://preview.example/g/shared/?t=example-token"})
	quoted, _ := json.Marshal(second)
	text := outputFileCitation(first) + "\n" + codexFileCitationPrefix + "{\npurpose=\"output\"\npath=" + string(quoted) + "\n}"
	result, err := p.RewriteFinalBlock(t.Context(), citationPreviewRequest(root, text))
	if err != nil || strings.Contains(result.Block.Text, codexFileCitationPrefix) ||
		!strings.Contains(result.Block.Text, "[first.docx](https://") || !strings.Contains(result.Block.Text, "[带`符号的说明书.docx](https://") {
		t.Fatalf("multiple citation rewrite failed: %q err=%v", result.Block.Text, err)
	}
}

func TestCodexOutputFileCitationKeepsAccessChecksAndAssistantBoundary(t *testing.T) {
	root := t.TempDir()
	outside := writePreviewFile(t, filepath.Join(t.TempDir(), "outside.docx"), "document bytes")
	p := NewDriveMarkdownPreviewer(newFakePreviewAPI(), MarkdownPreviewConfig{ProcessCWD: root, CacheDir: t.TempDir()})
	web := &fakeWebPreviewPublisher{baseURL: "https://preview.example/g/shared/?t=example-token"}
	p.SetWebPreviewPublisher(web)
	for _, req := range []FinalBlockPreviewRequest{
		citationPreviewRequest(root, outputFileCitation(outside)),
		func() FinalBlockPreviewRequest {
			req := citationPreviewRequest(root, outputFileCitation(outside))
			req.Block.Final = false
			return req
		}(),
		func() FinalBlockPreviewRequest {
			req := citationPreviewRequest(root, outputFileCitation(outside))
			req.Block.Kind = render.BlockKind("user_message")
			return req
		}(),
	} {
		result, err := p.RewriteFinalBlock(t.Context(), req)
		if err != nil || result.Block.Text != req.Block.Text || len(web.issuedFor) != 0 {
			t.Fatalf("access or final-output boundary bypassed: %#v err=%v", result, err)
		}
	}
}

func outputFileCitation(file string) string {
	quoted, _ := json.Marshal(file)
	return codexFileCitationPrefix + "{path=" + string(quoted) + ` purpose="output"}`
}

func citationPreviewRequest(root, text string) FinalBlockPreviewRequest {
	return FinalBlockPreviewRequest{
		GatewayID: "app-1", SurfaceSessionID: "feishu:app-1:user:ou_user", ActorUserID: "ou_user",
		WorkspaceRoot: root, ThreadCWD: root,
		Block: render.Block{Kind: render.BlockAssistantMarkdown, Final: true, Text: text},
	}
}

type citationReferenceProbe struct{ refs []PreviewReference }

func (*citationReferenceProbe) ID() string                                            { return "citation_probe" }
func (*citationReferenceProbe) Match(FinalBlockPreviewRequest, PreviewReference) bool { return true }
func (p *citationReferenceProbe) Plan(_ context.Context, _ FinalBlockPreviewRequest, ref PreviewReference) (*PreviewPlan, bool, error) {
	p.refs = append(p.refs, ref)
	return nil, false, nil
}
