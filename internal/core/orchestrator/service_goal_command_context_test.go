package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
)

func TestBoundGoalPageFormContextRoundTripsOnce(t *testing.T) {
	svc, surfaceID, _ := goalInterlockTestSetup(t)
	surface := svc.root.Surfaces[surfaceID]
	page := svc.boundGoalPage(surface, "user-1", "thread-1", "new", svc.goalCommandFormPage(surface, "new"))
	form := findCommandForm(&page)
	if form == nil {
		t.Fatalf("expected bound Goal form: %#v", page)
	}
	nonce := goalContextNonceFromCommandForTest(t, form.CommandText)
	if nonce == "" || nonce == "unavailable" {
		t.Fatalf("expected opaque Goal context nonce, command=%q", form.CommandText)
	}

	replayedText := form.CommandText + " 完成登录重构 --budget 900"
	parts, ok := svc.validateGoalContext(surface, goalContextActionForTest(surfaceID, "user-1", replayedText), strings.Fields(replayedText), "new")
	if !ok {
		t.Fatal("expected first form replay to validate")
	}
	if got, want := strings.Join(parts, " "), "/goal new 完成登录重构 --budget 900"; got != want {
		t.Fatalf("cleaned command = %q, want %q", got, want)
	}
	if _, replayOK := svc.validateGoalContext(surface, goalContextActionForTest(surfaceID, "user-1", replayedText), strings.Fields(replayedText), "new"); replayOK {
		t.Fatal("the same Goal form context must not be replayable")
	}
}

func TestBoundGoalPagePrunesAbandonedExpiredContexts(t *testing.T) {
	svc, surfaceID, _ := goalInterlockTestSetup(t)
	surface := svc.root.Surfaces[surfaceID]
	svc.pendingGoalFingerprints = map[string]pendingGoalFingerprint{
		"expired": {CapturedAt: svc.now().Add(-goalPendingFingerprintTTL - time.Second)},
		"fresh":   {CapturedAt: svc.now()},
	}
	svc.boundGoalPage(surface, "user-1", "thread-1", "new", svc.goalCommandFormPage(surface, "new"))
	if _, exists := svc.pendingGoalFingerprints["expired"]; exists {
		t.Fatal("abandoned expired context leaked")
	}
	if _, exists := svc.pendingGoalFingerprints["fresh"]; !exists {
		t.Fatal("fresh context was discarded")
	}
}

func TestBoundGoalPageClearContextOnlyBindsConfirm(t *testing.T) {
	svc, surfaceID, _ := goalInterlockTestSetup(t)
	surface := svc.root.Surfaces[surfaceID]
	page := svc.boundGoalPage(surface, "user-1", "thread-1", "clear", svc.goalClearConfirmPage(surface))

	confirmCommand := ""
	for _, button := range page.RelatedButtons {
		if strings.Contains(button.CommandText, "--confirm") {
			confirmCommand = button.CommandText
			continue
		}
		if strings.Contains(button.CommandText, goalContextFlagPrefix) {
			t.Fatalf("non-confirm button received Goal context: %#v", button)
		}
	}
	if confirmCommand == "" {
		t.Fatalf("expected bound clear confirmation: %#v", page.RelatedButtons)
	}
	goalContextNonceFromCommandForTest(t, confirmCommand)
	parts, ok := svc.validateGoalContext(surface, goalContextActionForTest(surfaceID, "user-1", confirmCommand), strings.Fields(confirmCommand), "clear")
	if !ok || strings.Join(parts, " ") != "/goal clear --confirm" {
		t.Fatalf("clear context validation = %v, parts=%q", ok, strings.Join(parts, " "))
	}
}

func TestGoalContextRejectsBindingMismatchAndConsumesNonce(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(surfaceID string, surfaceAction *control.Action, surfaceSelection *goalContextSurfaceMutation)
		sub    string
		text   func(flag string) string
	}{
		{
			name: "surface",
			mutate: func(_ string, action *control.Action, _ *goalContextSurfaceMutation) {
				action.SurfaceSessionID = "another-surface"
			},
		},
		{
			name: "actor",
			mutate: func(_ string, action *control.Action, _ *goalContextSurfaceMutation) {
				action.ActorUserID = "another-user"
			},
		},
		{
			name: "instance",
			mutate: func(_ string, _ *control.Action, selection *goalContextSurfaceMutation) {
				selection.instanceID = "another-instance"
			},
		},
		{
			name: "thread",
			mutate: func(_ string, _ *control.Action, selection *goalContextSurfaceMutation) {
				selection.threadID = "thread-2"
			},
		},
		{
			name: "operation clear",
			sub:  "clear",
			text: func(flag string) string { return "/goal clear --confirm " + flag },
		},
		{
			name: "operation pause",
			sub:  "pause",
			text: func(flag string) string { return "/goal pause " + flag },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, surfaceID, _ := goalInterlockTestSetup(t)
			surface := svc.root.Surfaces[surfaceID]
			page := svc.boundGoalPage(surface, "user-1", "thread-1", "new", svc.goalCommandFormPage(surface, "new"))
			form := findCommandForm(&page)
			if form == nil {
				t.Fatal("expected Goal form")
			}
			flag := goalContextFlagPrefix + goalContextNonceFromCommandForTest(t, form.CommandText)
			text := form.CommandText + " ship it"
			sub := "new"
			if test.text != nil {
				text = test.text(flag)
			}
			if test.sub != "" {
				sub = test.sub
			}
			action := goalContextActionForTest(surfaceID, "user-1", text)
			selection := goalContextSurfaceMutation{
				instanceID: surface.AttachedInstanceID,
				threadID:   surface.SelectedThreadID,
			}
			if test.mutate != nil {
				test.mutate(surfaceID, &action, &selection)
			}
			surface.AttachedInstanceID = selection.instanceID
			surface.SelectedThreadID = selection.threadID

			if _, ok := svc.validateGoalContext(surface, action, strings.Fields(text), sub); ok {
				t.Fatalf("mismatched %s context unexpectedly validated", test.name)
			}
			surface.AttachedInstanceID = "inst-1"
			surface.SelectedThreadID = "thread-1"
			if _, ok := svc.validateGoalContext(surface, goalContextActionForTest(surfaceID, "user-1", form.CommandText+" ship it"), strings.Fields(form.CommandText+" ship it"), "new"); ok {
				t.Fatal("a context rejected for mismatch must already be consumed")
			}
		})
	}
}

func TestGoalContextRejectsMultipleClearContexts(t *testing.T) {
	svc, surfaceID, _ := goalInterlockTestSetup(t)
	surface := svc.root.Surfaces[surfaceID]
	page := svc.boundGoalPage(surface, "user-1", "thread-1", "clear", svc.goalClearConfirmPage(surface))
	confirmCommand := ""
	for _, button := range page.RelatedButtons {
		if strings.Contains(button.CommandText, "--confirm") {
			confirmCommand = button.CommandText
			break
		}
	}
	if confirmCommand == "" {
		t.Fatal("expected bound clear confirmation")
	}
	nonce := goalContextNonceFromCommandForTest(t, confirmCommand)
	text := confirmCommand + " " + goalContextFlagPrefix + nonce
	if _, ok := svc.validateGoalContext(surface, goalContextActionForTest(surfaceID, "user-1", text), strings.Fields(text), "clear"); ok {
		t.Fatal("clear callback with multiple Goal contexts unexpectedly validated")
	}
}

func TestGoalBoundCallbackCannotMutateAfterThreadSwitch(t *testing.T) {
	for _, test := range []struct {
		name        string
		openCommand string
		callback    func(t *testing.T, page *control.FeishuPageView) string
		mutation    agentproto.CommandKind
	}{
		{
			name:        "form",
			openCommand: "/goal edit",
			callback: func(t *testing.T, page *control.FeishuPageView) string {
				form := findCommandForm(page)
				if form == nil {
					t.Fatalf("expected edit form: %#v", page)
				}
				return form.CommandText + " updated goal"
			},
			mutation: agentproto.CommandThreadGoalSet,
		},
		{
			name:        "clear",
			openCommand: "/goal clear",
			callback:    goalClearConfirmCommandForTest,
			mutation:    agentproto.CommandThreadGoalClear,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, surfaceID, _ := goalInterlockTestSetup(t)
			openEvents := svc.ApplySurfaceAction(goalContextActionForTest(surfaceID, "user-1", test.openCommand))
			callbackText := test.callback(t, findPageEvent(openEvents))

			inst := svc.root.Instances["inst-1"]
			thread2 := *inst.Threads["thread-1"]
			thread2.ThreadID = "thread-2"
			inst.Threads["thread-2"] = &thread2
			svc.ApplySurfaceAction(control.Action{
				Kind:             control.ActionUseThread,
				SurfaceSessionID: surfaceID,
				ActorUserID:      "user-1",
				ThreadID:         "thread-2",
			})
			if selected := svc.root.Surfaces[surfaceID].SelectedThreadID; selected != "thread-2" {
				t.Fatalf("thread switch failed, selected=%q", selected)
			}

			result := svc.ApplySurfaceAction(goalContextActionForTest(surfaceID, "user-1", callbackText))
			if findAgentCommand(result, test.mutation) != nil {
				t.Fatalf("stale %s callback mutated the newly selected thread: %#v", test.name, result)
			}
			page := findPageEvent(result)
			if page == nil || page.StatusKind != "error" {
				t.Fatalf("stale %s callback did not fail closed: %#v", test.name, result)
			}
			if len(svc.pendingGoalFingerprints) != 0 {
				t.Fatalf("stale %s context was not consumed: %#v", test.name, svc.pendingGoalFingerprints)
			}
		})
	}
}

func TestGoalContextRejectsExpiredAndUnknownNonce(t *testing.T) {
	svc, surfaceID, _ := goalInterlockTestSetup(t)
	surface := svc.root.Surfaces[surfaceID]
	clock := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return clock }
	page := svc.boundGoalPage(surface, "user-1", "thread-1", "edit", svc.goalCommandFormPage(surface, "edit"))
	form := findCommandForm(&page)
	if form == nil {
		t.Fatal("expected Goal edit form")
	}
	clock = clock.Add(goalPendingFingerprintTTL + time.Nanosecond)
	text := form.CommandText + " updated goal"
	if _, ok := svc.validateGoalContext(surface, goalContextActionForTest(surfaceID, "user-1", text), strings.Fields(text), "edit"); ok {
		t.Fatal("expired Goal context unexpectedly validated")
	}

	unknown := "/goal edit --goal-context=00000000000000000000000000000000 updated goal"
	if _, ok := svc.validateGoalContext(surface, goalContextActionForTest(surfaceID, "user-1", unknown), strings.Fields(unknown), "edit"); ok {
		t.Fatal("unknown Goal context unexpectedly validated")
	}
}

func TestGoalContextFingerprintRequiresExactBudgetPresence(t *testing.T) {
	for _, test := range []struct {
		name   string
		before *int64
		after  *int64
	}{
		{name: "nil to value", before: nil, after: goalContextBudgetForTest(500)},
		{name: "value to nil", before: goalContextBudgetForTest(500), after: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, surfaceID, _ := goalInterlockTestSetup(t)
			surface := svc.root.Surfaces[surfaceID]
			goal := svc.root.Instances["inst-1"].Threads["thread-1"].ThreadGoal
			goal.TokenBudget = test.before
			page := svc.boundGoalPage(surface, "user-1", "thread-1", "edit", svc.goalCommandFormPage(surface, "edit"))
			form := findCommandForm(&page)
			if form == nil {
				t.Fatal("expected Goal edit form")
			}
			goal.TokenBudget = test.after
			text := form.CommandText + " updated goal"
			if _, ok := svc.validateGoalContext(surface, goalContextActionForTest(surfaceID, "user-1", text), strings.Fields(text), "edit"); ok {
				t.Fatalf("budget-presence drift unexpectedly validated: before=%v after=%v", test.before, test.after)
			}
		})
	}
}

func TestGoalContextWithoutFlagKeepsLegacyFingerprintPath(t *testing.T) {
	svc, surfaceID, _ := goalInterlockTestSetup(t)
	surface := svc.root.Surfaces[surfaceID]
	svc.captureGoalFingerprint(surface, "thread-1", "edit")
	parts := strings.Fields("/goal edit updated goal")
	cleaned, ok := svc.validateGoalContext(surface, goalContextActionForTest(surfaceID, "user-1", strings.Join(parts, " ")), parts, "edit")
	if !ok || strings.Join(cleaned, " ") != strings.Join(parts, " ") {
		t.Fatalf("legacy no-context validation = %v, parts=%q", ok, strings.Join(cleaned, " "))
	}
	if len(svc.pendingGoalFingerprints) != 1 {
		t.Fatalf("no-context helper must leave legacy fingerprint for the existing switch validation, got %#v", svc.pendingGoalFingerprints)
	}
}

func TestGoalContextFlagInsideObjectiveIsPreserved(t *testing.T) {
	svc, surfaceID, _ := goalInterlockTestSetup(t)
	surface := svc.root.Surfaces[surfaceID]
	page := svc.boundGoalPage(surface, "user-1", "thread-1", "new", svc.goalCommandFormPage(surface, "new"))
	form := findCommandForm(&page)
	if form == nil {
		t.Fatal("expected Goal form")
	}
	text := form.CommandText + " objective --goal-context=literal tail"
	cleaned, ok := svc.validateGoalContext(surface, goalContextActionForTest(surfaceID, "user-1", text), strings.Fields(text), "new")
	if want := "/goal new objective --goal-context=literal tail"; !ok || strings.Join(cleaned, " ") != want {
		t.Fatalf("literal objective flag changed: ok=%v parts=%q", ok, strings.Join(cleaned, " "))
	}
}

type goalContextSurfaceMutation struct {
	instanceID string
	threadID   string
}

func goalContextActionForTest(surfaceID, actorUserID, text string) control.Action {
	return control.Action{
		Kind:             control.ActionGoalCommand,
		SurfaceSessionID: surfaceID,
		ActorUserID:      actorUserID,
		Text:             text,
	}
}

func goalContextNonceFromCommandForTest(t *testing.T, commandText string) string {
	t.Helper()
	for _, part := range strings.Fields(commandText) {
		if strings.HasPrefix(part, goalContextFlagPrefix) {
			nonce := strings.TrimPrefix(part, goalContextFlagPrefix)
			if nonce == "" {
				t.Fatalf("empty Goal context in %q", commandText)
			}
			return nonce
		}
	}
	t.Fatalf("Goal context missing from %q", commandText)
	return ""
}

func goalContextBudgetForTest(value int64) *int64 {
	return &value
}
