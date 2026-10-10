package control

import "testing"

func TestScheduleCommandRoutesToDaemonWithoutNewCardForm(t *testing.T) {
	for _, text := range []string{"/schedule", "/SCHEDULE list", "/schedule pause task_1", "/schedule resume task_1", "/schedule delete task_1"} {
		action, ok := ParseFeishuTextActionWithoutCatalog(text)
		if !ok || action.Kind != ActionScheduleCommand || action.CommandID != FeishuCommandSchedule || action.Text != text {
			t.Fatalf("parse %q = %#v, %v", text, action, ok)
		}
		binding, ok := ResolveFeishuCommandBindingFromAction(action)
		if !ok || binding.DirectDaemonCommand != DaemonCommandSchedule || binding.Kind != FeishuCommandBindingDaemonCommand {
			t.Fatalf("binding = %#v", binding)
		}
	}
	if _, ok := ParseFeishuTextActionWithoutCatalog("/scheduled list"); ok {
		t.Fatal("prefix collision")
	}
	action, ok := ParseFeishuMenuActionWithoutCatalog("schedule")
	if !ok || action.Kind != ActionScheduleCommand || action.Text != "/schedule" {
		t.Fatalf("menu = %#v", action)
	}
	if form, ok := FeishuCommandForm(FeishuCommandSchedule); ok || form != nil {
		t.Fatalf("unexpected card form: %#v", form)
	}
}

func TestScheduleCommandMutationsAreImmediateAndHeadlessOnly(t *testing.T) {
	for _, text := range []string{"/schedule pause task_1", "/schedule resume task_1", "/schedule delete task_1"} {
		if !scheduleCommandRunsImmediately(text) {
			t.Fatalf("mutation %q classified as navigation", text)
		}
	}
	for _, text := range []string{"/schedule", "/schedule help", "/schedule list", "/schedule pause"} {
		if scheduleCommandRunsImmediately(text) {
			t.Fatalf("unexpected mutation: %q", text)
		}
	}
	for _, mode := range []string{"normal", "vscode"} {
		support, ok := ResolveFeishuCommandSupport(CatalogContext{ProductMode: mode}, FeishuCommandSchedule)
		if !ok || support.DispatchAllowed != (mode == "normal") {
			t.Fatalf("%s support: %#v", mode, support)
		}
	}
}
