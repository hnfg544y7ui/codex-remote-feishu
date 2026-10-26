package preview

import (
	"context"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/markdown"
)

func (p *DriveMarkdownPreviewer) rewriteMarkdownLinksCodeSpan(
	ctx context.Context,
	req FinalBlockPreviewRequest,
	principals []previewPrincipal,
	runtime *previewRewriteRuntime,
	scopeKey string,
	rewrittenTargets map[string]string,
	rawSpan string,
	text string,
	baseOffset int,
) (string, bool, []string) {
	if text == "" {
		return rawSpan, false, nil
	}
	trimStart, trimEnd := trimMarkdownInlineSpaceBounds(text)
	if trimStart >= trimEnd {
		return rawSpan, false, nil
	}
	trimmed := text[trimStart:trimEnd]
	if strings.Contains(trimmed, codexFileCitationPrefix) {
		return rawSpan, false, nil
	}
	if end, label, rawTarget, ok := markdown.ParseMarkdownLinkAt(trimmed, 0); ok && end == len(trimmed) {
		replacement, rewrittenChanged, rewrittenErrs := p.rewritePreviewReferenceTarget(
			ctx,
			req,
			principals,
			runtime,
			scopeKey,
			rewrittenTargets,
			rawTarget,
			baseOffset+trimStart+len(label)+3,
			baseOffset+trimStart+len(label)+3+len(rawTarget),
		)
		if !rewrittenChanged {
			return rawSpan, false, rewrittenErrs
		}
		return "[" + label + "](" + replacement + ")", true, rewrittenErrs
	}
	rawTarget, display, ok := parseStandalonePreviewReferenceWhole(trimmed)
	if !ok {
		return rawSpan, false, nil
	}
	replacement, rewrittenChanged, rewrittenErrs := p.rewritePreviewReferenceTarget(
		ctx,
		req,
		principals,
		runtime,
		scopeKey,
		rewrittenTargets,
		rawTarget,
		baseOffset+trimStart,
		baseOffset+trimStart+len(rawTarget),
	)
	if !rewrittenChanged {
		return rawSpan, false, rewrittenErrs
	}
	return "[" + display + "](" + replacement + ")", true, rewrittenErrs
}
