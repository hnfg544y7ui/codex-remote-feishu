package feishu

import (
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/markdown"
)

// Feishu interprets Markdown image destinations as uploaded image keys, not
// URLs. Keep external images accessible as links without fetching untrusted
// content. Run on the rendered copy so transport and size checks see the same
// payload, while the model source and callback values remain unchanged.
func normalizeCardMarkdownImages(value any) {
	switch typed := value.(type) {
	case map[string]any:
		if typed["tag"] == "markdown" || typed["tag"] == cardTextTagLarkMarkdown {
			if content, ok := typed["content"].(string); ok {
				typed["content"] = externalMarkdownImagesAsLinks(content)
			}
		}
		for key, child := range typed {
			if key != "value" && key != "behaviors" {
				normalizeCardMarkdownImages(child)
			}
		}
	case []map[string]any:
		for _, child := range typed {
			normalizeCardMarkdownImages(child)
		}
	case []any:
		for _, child := range typed {
			normalizeCardMarkdownImages(child)
		}
	}
}

func externalMarkdownImagesAsLinks(text string) string {
	var out strings.Builder
	for _, segment := range markdown.SplitFenceSegments(text) {
		if segment.Fenced {
			out.WriteString(segment.Text)
		} else {
			out.WriteString(externalMarkdownImagesInline(segment.Text))
		}
	}
	return out.String()
}

func externalMarkdownImagesInline(text string) string {
	var out strings.Builder
	for i := 0; i < len(text); {
		if text[i] == '\\' && i+1 < len(text) {
			out.WriteString(text[i : i+2])
			i += 2
			continue
		}
		if text[i] == '`' {
			run := markdown.ConsecutiveByteRun(text, i, '`')
			close := closingImageBackticks(text, i+run, run)
			if close >= 0 {
				out.WriteString(text[i : close+run])
				i = close + run
				continue
			}
			iEnd := i + run
			out.WriteString(text[i:iEnd])
			i = iEnd
			continue
		}
		if text[i] == '!' && i+1 < len(text) && text[i+1] == '[' {
			end, labelEnd, target, ok := parseCardMarkdownImage(text, i)
			if ok {
				url := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(target), "<"))
				if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
					label := text[i+2 : labelEnd]
					if strings.TrimSpace(label) == "" {
						label = "查看图片"
					}
					out.WriteByte('[')
					out.WriteString(label)
					out.WriteString(text[labelEnd:end])
				} else {
					out.WriteString(text[i:end])
				}
				i = end
				continue
			}
		}
		out.WriteByte(text[i])
		i++
	}
	return out.String()
}

func closingImageBackticks(text string, start, run int) int {
	for i := start; i < len(text); {
		if text[i] != '`' {
			i++
			continue
		}
		count := markdown.ConsecutiveByteRun(text, i, '`')
		if count == run {
			return i
		}
		i += count
	}
	return -1
}

// Labels may contain nested or escaped brackets. Destinations may contain
// balanced/escaped parentheses; preserve their original spelling byte for byte.
func parseCardMarkdownImage(text string, start int) (end, labelEnd int, target string, ok bool) {
	depth := 1
	for i := start + 2; i < len(text); i++ {
		switch text[i] {
		case '\\':
			i++
		case '[':
			depth++
		case ']':
			depth--
			if depth != 0 {
				continue
			}
			labelEnd = i
			if i+1 >= len(text) || text[i+1] != '(' {
				return 0, 0, "", false
			}
			targetStart := i + 2
			depth = 0
			for j := targetStart; j < len(text); j++ {
				switch text[j] {
				case '\\':
					j++
				case '(':
					depth++
				case ')':
					if depth == 0 {
						return j + 1, labelEnd, text[targetStart:j], true
					}
					depth--
				}
			}
			return 0, 0, "", false
		}
	}
	return 0, 0, "", false
}
