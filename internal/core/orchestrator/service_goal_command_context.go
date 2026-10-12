package orchestrator

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

const (
	goalContextFlagPrefix = "--goal-context="
	goalContextKeyPrefix  = "goal-context|"
)

// boundGoalPage binds the mutating control in a Goal form or clear-confirm
// page to the exact surface, actor, instance, thread, and Goal snapshot that
// produced it. The nonce travels through the existing CommandText replay.
func (s *Service) boundGoalPage(surface *state.SurfaceConsoleRecord, actorUserID, threadID, sub string, page control.FeishuPageView) control.FeishuPageView {
	if surface == nil || strings.TrimSpace(threadID) == "" || strings.TrimSpace(surface.SelectedThreadID) != strings.TrimSpace(threadID) {
		return page
	}
	sub = strings.ToLower(strings.TrimSpace(sub))
	if sub != "new" && sub != "edit" && sub != "clear" {
		return page
	}

	nonce, ok := newGoalContextNonce()
	if !ok {
		// Keep the rendered control fail-closed even if the operating system's
		// random source is unavailable. No matching pending entry is stored.
		nonce = "unavailable"
	}
	contextFlag := goalContextFlagPrefix + nonce
	bound := false
	if sub == "new" || sub == "edit" {
		for sectionIndex := range page.Sections {
			for entryIndex := range page.Sections[sectionIndex].Entries {
				form := page.Sections[sectionIndex].Entries[entryIndex].Form
				if form == nil {
					continue
				}
				if commandText, matches := bindGoalCommandText(form.CommandText, sub, contextFlag, false); matches {
					form.CommandText = commandText
					bound = true
				}
			}
		}
	} else {
		for buttonIndex := range page.RelatedButtons {
			button := &page.RelatedButtons[buttonIndex]
			if commandText, matches := bindGoalCommandText(button.CommandText, sub, contextFlag, true); matches {
				button.CommandText = commandText
				bound = true
			}
		}
	}
	if !bound || !ok {
		return page
	}

	if s.pendingGoalFingerprints == nil {
		s.pendingGoalFingerprints = map[string]pendingGoalFingerprint{}
	}
	// Abandoned forms must not accumulate for the lifetime of the daemon.
	now := s.now()
	for key, pending := range s.pendingGoalFingerprints {
		if pending.CapturedAt.IsZero() || now.Before(pending.CapturedAt) || now.Sub(pending.CapturedAt) > goalPendingFingerprintTTL {
			delete(s.pendingGoalFingerprints, key)
		}
	}
	s.pendingGoalFingerprints[goalContextPendingKey(nonce)] = pendingGoalFingerprint{
		Fingerprint:  goalContextFingerprint(s.surfaceGoalForPage(surface)),
		CapturedAt:   now,
		ContextNonce: nonce,
		SurfaceID:    strings.TrimSpace(surface.SurfaceSessionID),
		InstanceID:   strings.TrimSpace(surface.AttachedInstanceID),
		ThreadID:     strings.TrimSpace(threadID),
		ActorUserID:  strings.TrimSpace(actorUserID),
		Sub:          sub,
	}
	return page
}

// validateGoalContext strips the private context flag before the normal Goal
// parser sees the command. Context-bearing callbacks are single-use and fail
// closed on every mismatch. Commands without the flag retain the legacy
// fingerprint behavior used by direct slash commands.
func (s *Service) validateGoalContext(surface *state.SurfaceConsoleRecord, action control.Action, parts []string, sub string) ([]string, bool) {
	cleaned, nonce, hasContext, wellFormed := stripGoalContextFlag(parts, sub)
	if !wellFormed {
		return cleaned, false
	}
	threadID := ""
	if surface != nil {
		threadID = strings.TrimSpace(surface.SelectedThreadID)
	}
	if !hasContext {
		return cleaned, true
	}
	if surface == nil || s.pendingGoalFingerprints == nil {
		return cleaned, false
	}

	key := goalContextPendingKey(nonce)
	pending, ok := s.pendingGoalFingerprints[key]
	if !ok {
		return cleaned, false
	}
	// Consume before validating so a wrong actor, stale page, or failed replay
	// cannot probe and then reuse the same callback.
	delete(s.pendingGoalFingerprints, key)
	delete(s.pendingGoalFingerprints, goalPendingFingerprintKey(pending.SurfaceID, pending.ThreadID, pending.Sub))

	now := s.now()
	if pending.CapturedAt.IsZero() || now.Before(pending.CapturedAt) || now.Sub(pending.CapturedAt) > goalPendingFingerprintTTL {
		return cleaned, false
	}
	if pending.ContextNonce != nonce ||
		pending.SurfaceID != strings.TrimSpace(surface.SurfaceSessionID) ||
		pending.SurfaceID != strings.TrimSpace(action.SurfaceSessionID) ||
		pending.InstanceID != strings.TrimSpace(surface.AttachedInstanceID) ||
		pending.ThreadID != threadID ||
		pending.ActorUserID != strings.TrimSpace(action.ActorUserID) ||
		pending.Sub != strings.ToLower(strings.TrimSpace(sub)) {
		return cleaned, false
	}
	if surfaceActor := strings.TrimSpace(surface.ActorUserID); surfaceActor != "" && pending.ActorUserID != surfaceActor {
		return cleaned, false
	}
	if !goalContextFingerprintMatches(pending.Fingerprint, s.surfaceGoalForPage(surface)) {
		return cleaned, false
	}
	return cleaned, true
}

func bindGoalCommandText(commandText, sub, contextFlag string, requireConfirm bool) (string, bool) {
	parts := strings.Fields(strings.TrimSpace(commandText))
	if len(parts) < 2 || !strings.EqualFold(parts[0], "/goal") || !strings.EqualFold(parts[1], sub) {
		return commandText, false
	}
	confirmed := false
	for _, part := range parts[2:] {
		if part == "--confirm" {
			confirmed = true
		}
		if part == "--goal-context" || strings.HasPrefix(part, goalContextFlagPrefix) {
			return commandText, false
		}
	}
	if requireConfirm && !confirmed {
		return commandText, false
	}
	return strings.TrimSpace(commandText) + " " + contextFlag, true
}

func stripGoalContextFlag(parts []string, sub string) (cleaned []string, nonce string, hasContext, wellFormed bool) {
	contextIndex := goalContextFlagIndex(parts, sub)
	if contextIndex < 0 {
		return append([]string(nil), parts...), "", false, true
	}
	if strings.EqualFold(strings.TrimSpace(sub), "clear") {
		for index, candidate := range parts {
			if index != contextIndex && (candidate == "--goal-context" || strings.HasPrefix(candidate, goalContextFlagPrefix)) {
				return append([]string(nil), parts[:contextIndex]...), "", true, false
			}
		}
	}
	part := parts[contextIndex]
	if part == "--goal-context" {
		return append([]string(nil), parts[:contextIndex]...), "", true, false
	}
	nonce = strings.TrimSpace(strings.TrimPrefix(part, goalContextFlagPrefix))
	if !validGoalContextNonce(nonce) {
		return append([]string(nil), parts[:contextIndex]...), "", true, false
	}
	cleaned = make([]string, 0, len(parts)-1)
	cleaned = append(cleaned, parts[:contextIndex]...)
	cleaned = append(cleaned, parts[contextIndex+1:]...)
	return cleaned, nonce, true, true
}

func goalContextFlagIndex(parts []string, sub string) int {
	if len(parts) <= 2 {
		return -1
	}
	isContextFlag := func(part string) bool {
		return part == "--goal-context" || strings.HasPrefix(part, goalContextFlagPrefix)
	}
	switch strings.ToLower(strings.TrimSpace(sub)) {
	case "new", "edit":
		if isContextFlag(parts[2]) {
			return 2
		}
	case "clear":
		for index := 2; index < len(parts); index++ {
			if isContextFlag(parts[index]) {
				return index
			}
			if parts[index] != "--confirm" {
				break
			}
		}
	default:
		for index := 2; index < len(parts); index++ {
			if isContextFlag(parts[index]) {
				return index
			}
		}
	}
	return -1
}

func validGoalContextNonce(nonce string) bool {
	if len(nonce) != 32 {
		return false
	}
	decoded, err := hex.DecodeString(nonce)
	return err == nil && len(decoded) == 16 && nonce == strings.ToLower(nonce)
}

func goalContextPendingKey(nonce string) string {
	return goalContextKeyPrefix + strings.TrimSpace(nonce)
}

func newGoalContextNonce() (string, bool) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", false
	}
	return hex.EncodeToString(raw[:]), true
}

func goalContextFingerprint(goal *agentproto.ThreadGoalUpdate) goalFingerprint {
	fingerprint := goalFingerprint{GoalMissing: goal == nil || goal.Cleared}
	if goal == nil || goal.Cleared {
		return fingerprint
	}
	fingerprint.CreatedAt = goal.CreatedAt
	fingerprint.Objective = strings.TrimSpace(goal.Objective)
	if goal.TokenBudget != nil {
		value := *goal.TokenBudget
		fingerprint.TokenBudget = &value
	}
	return fingerprint
}

func goalContextFingerprintMatches(fingerprint goalFingerprint, goal *agentproto.ThreadGoalUpdate) bool {
	if fingerprint.GoalMissing {
		return goal == nil || goal.Cleared
	}
	if goal == nil || goal.Cleared || fingerprint.CreatedAt != goal.CreatedAt || fingerprint.Objective != strings.TrimSpace(goal.Objective) {
		return false
	}
	if (fingerprint.TokenBudget == nil) != (goal.TokenBudget == nil) {
		return false
	}
	return fingerprint.TokenBudget == nil || *fingerprint.TokenBudget == *goal.TokenBudget
}
