package orchestrator

import "strings"

// guardianWarning currently supplies only threadId and message, not a typed
// decision. Recognize the captured approval header exactly; unknown formats
// remain visible. This classifies display only, never grants tool approval.
func isGuardianApprovedReview(summary string) bool {
	const prefix = "Automatic approval review approved (risk: "
	summary = strings.TrimSpace(summary)
	if !strings.HasPrefix(summary, prefix) {
		return false
	}
	header, _, complete := strings.Cut(strings.TrimPrefix(summary, prefix), "):")
	if !complete {
		return false
	}
	risk, authorization, complete := strings.Cut(header, ", authorization: ")
	if !complete {
		return false
	}
	switch risk {
	case "low", "medium", "high":
	default:
		return false
	}
	switch authorization {
	case "low", "medium", "high":
		return true
	default:
		return false
	}
}
