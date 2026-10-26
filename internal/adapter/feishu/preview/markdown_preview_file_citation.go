package preview

import (
	"context"
	"encoding/json"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const codexFileCitationPrefix = ":codex-file-citation"

type codexOutputFileCitation struct {
	end, pathStart, pathEnd int
	path                    string
	valid                   bool
}

func (p *DriveMarkdownPreviewer) rewriteCodexOutputFileCitation(
	ctx context.Context,
	req FinalBlockPreviewRequest,
	principals []previewPrincipal,
	runtime *previewRewriteRuntime,
	scopeKey string,
	rewrittenTargets map[string]string,
	text string,
	start, baseOffset int,
	citation codexOutputFileCitation,
) (string, bool, []string) {
	if !citation.valid {
		return text[start:citation.end], false, nil
	}
	replacement, changed, errs := p.rewritePreviewReferenceTarget(
		ctx, req, principals, runtime, scopeKey, rewrittenTargets, citation.path,
		baseOffset+citation.pathStart, baseOffset+citation.pathEnd,
	)
	if !changed {
		return text[start:citation.end], false, errs
	}
	label := path.Base(strings.ReplaceAll(citation.path, `\`, "/"))
	label = strings.NewReplacer(`\`, `\\`, "[", `\[`, "]", `\]`).Replace(label)
	return "[" + label + "](" + replacement + ")", true, errs
}

// Recognized annotations are consumed as a unit even when malformed or of an
// unsupported purpose. This prevents the generic path/link scanner from
// publishing a path inside an attribute and corrupting the native annotation.
// An unterminated annotation conservatively protects the remaining segment.
func parseCodexOutputFileCitation(text string, start int) (codexOutputFileCitation, bool) {
	// Protect a Markdown-escaped annotation before a leading backslash can be
	// mistaken for a standalone Windows path by the generic scanner.
	literal := text[start] == '\\'
	if literal {
		for start < len(text) && text[start] == '\\' {
			start++
		}
	}
	if !strings.HasPrefix(text[start:], codexFileCitationPrefix) {
		return codexOutputFileCitation{}, false
	}
	open := skipCitationSpace(text, start+len(codexFileCitationPrefix))
	if open >= len(text) || text[open] != '{' {
		return codexOutputFileCitation{}, false
	}
	citation := codexOutputFileCitation{end: citationAnnotationEnd(text, open+1)}
	if citation.end <= open+1 || text[citation.end-1] != '}' || literal || citationEscaped(text, start) {
		return citation, true
	}
	attrs := map[string]string{}
	for i := open + 1; ; {
		i = skipCitationSpace(text, i)
		if i == citation.end-1 {
			break
		}
		nameStart := i
		for i < citation.end-1 && text[i] >= 'a' && text[i] <= 'z' {
			i++
		}
		name := text[nameStart:i]
		if name != "path" && name != "purpose" {
			return citation, true
		}
		if _, exists := attrs[name]; exists {
			return citation, true
		}
		i = skipCitationSpace(text, i)
		if i >= citation.end-1 || text[i] != '=' {
			return citation, true
		}
		i = skipCitationSpace(text, i+1)
		if i >= citation.end-1 || text[i] != '"' {
			return citation, true
		}
		valueStart := i + 1
		valueEnd := citationQuotedEnd(text, i)
		if valueEnd < 0 || valueEnd >= citation.end-1 {
			return citation, true
		}
		// JSON escapes preserve Windows paths and Unicode. Literal surrounding
		// whitespace is tolerated, but embedded control characters are not.
		rawValue := strings.TrimSpace(text[valueStart:valueEnd])
		var decoded string
		if json.Unmarshal([]byte(`"`+rawValue+`"`), &decoded) != nil {
			return citation, true
		}
		attrs[name] = strings.TrimSpace(decoded)
		if name == "path" {
			citation.pathStart, citation.pathEnd = valueStart, valueEnd
		}
		i = valueEnd + 1
		next := skipCitationSpace(text, i)
		if next == i && i != citation.end-1 {
			return citation, true
		}
	}
	citation.path = attrs["path"]
	citation.valid = attrs["purpose"] == "output" && citationAbsolutePath(citation.path)
	return citation, true
}

func citationAnnotationEnd(text string, start int) int {
	for i := start; i < len(text); i++ {
		if text[i] == '"' {
			end := citationQuotedEnd(text, i)
			if end < 0 {
				return len(text)
			}
			i = end
		} else if text[i] == '}' {
			return i + 1
		}
	}
	return len(text)
}

func citationQuotedEnd(text string, start int) int {
	for i := start + 1; i < len(text); i++ {
		if text[i] == '\\' {
			i++
		} else if text[i] == '"' {
			return i
		}
	}
	return -1
}

func skipCitationSpace(text string, start int) int {
	for start < len(text) {
		r, size := utf8.DecodeRuneInString(text[start:])
		if !unicode.IsSpace(r) {
			break
		}
		start += size
	}
	return start
}

func citationEscaped(text string, start int) bool {
	count := 0
	for i := start - 1; i >= 0 && text[i] == '\\'; i-- {
		count++
	}
	return count%2 != 0
}

func citationAbsolutePath(value string) bool {
	return strings.HasPrefix(value, "/") || filepath.IsAbs(value) ||
		(len(value) > 2 && value[1] == ':' && (value[2] == '\\' || value[2] == '/'))
}
