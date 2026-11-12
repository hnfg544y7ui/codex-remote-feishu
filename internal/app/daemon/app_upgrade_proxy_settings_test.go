package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/app/daemon/codexupgraderuntime"
	"github.com/kxn/codex-remote-feishu/internal/app/install"
	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

func upgradeProxyFixture(t *testing.T) (*App, string, string) {
	t.Helper()
	a, statePath := newUpgradeTestApp(t, newLifecycleGateway())
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"version":2,"feishu":{"useSystemProxy":false,"apps":[{"appSecret":"keep-me"}]},"unknown":"unchanged"}`), 0600); err != nil {
		t.Fatal(err)
	}
	a.admin.loadConfig = func() (config.LoadedAppConfig, error) { return config.LoadAppConfigAtPath(path) }
	return a, path, statePath
}

func upgradeProxyPage(t *testing.T, events []eventcontract.Event) control.FeishuPageView {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("events=%#v", events)
	}
	payload, ok := events[0].Payload.(eventcontract.PagePayload)
	if !ok {
		t.Fatalf("expected page, got %#v", events[0])
	}
	return payload.View
}

func TestUpgradeProxySettingsPersistAndExposeBothChoices(t *testing.T) {
	a, path, _ := upgradeProxyFixture(t)
	if mode, err := a.upgradeProxyMode(); err != nil || mode != "system" {
		t.Fatalf("default=%q %v", mode, err)
	}
	a.mu.Lock()
	events := a.handleUpgradeDaemonCommand(control.DaemonCommand{SurfaceSessionID: "s", Text: "/upgrade proxy direct"})
	a.mu.Unlock()
	view := upgradeProxyPage(t, events)
	if view.Title != "升级网络" || !strings.Contains(upgradeProxyNoticeText(view), "设置已保存") {
		t.Fatalf("unexpected page title=%q notice=%q", view.Title, upgradeProxyNoticeText(view))
	}
	if mode, err := a.upgradeProxyMode(); err != nil || mode != "direct" {
		t.Fatalf("saved=%q %v", mode, err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "keep-me") || !strings.Contains(string(raw), "unchanged") {
		t.Fatal("credentials or unrelated settings lost")
	}
	for _, mode := range []string{"system", "direct"} {
		page := buildUpgradeProxyPageView(mode, "", "")
		buttons := page.Sections[0].Entries[0].Buttons
		if len(buttons) != 2 {
			t.Fatalf("buttons=%#v", buttons)
		}
		for _, button := range buttons {
			if button.Disabled != strings.HasSuffix(button.CommandText, mode) {
				t.Fatalf("wrong current selection: %#v", button)
			}
			if button.Kind != control.CommandCatalogButtonCallbackAction || len(button.CallbackValue) == 0 {
				t.Fatal("invalid callback")
			}
		}
	}
	rootFound := false
	for _, b := range buildUpgradeRootButtons(false) {
		if b.CommandText == "/upgrade proxy" && b.Label == "升级网络" {
			rootFound = true
		}
	}
	if !rootFound {
		t.Fatal("root entry missing")
	}
}

func TestUpgradeProxySettingsRejectChangesDuringMaintenance(t *testing.T) {
	cases := []struct {
		name  string
		apply func(*App, string)
	}{
		{"remote-check", func(a *App, _ string) { a.upgradeRuntime.CheckInFlight = true }},
		{"remote-download", func(a *App, _ string) { a.upgradeRuntime.StartInFlight = true }},
		{"codex-upgrade", func(a *App, _ string) { a.codexUpgradeRuntime.Active = &codexupgraderuntime.Transaction{} }},
		{"codex-check", func(a *App, _ string) {
			a.codexUpgradeRuntime.ActiveFlow = &codexupgraderuntime.OwnerCardFlowRecord{Stage: codexupgraderuntime.OwnerFlowStageChecking}
		}},
		{"switching", func(a *App, p string) {
			s, e := install.LoadState(p)
			if e != nil {
				t.Fatal(e)
			}
			s.PendingUpgrade = &install.PendingUpgrade{Phase: install.PendingUpgradePhaseSwitching}
			if e = install.WriteState(p, s); e != nil {
				t.Fatal(e)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, path, statePath := upgradeProxyFixture(t)
			before, _ := os.ReadFile(path)
			tc.apply(a, statePath)
			a.mu.Lock()
			events := a.upgradeProxySettingsEventsLocked("s", "direct")
			// Read-only navigation stays available even while upgrade work is active.
			readEvents := a.upgradeProxySettingsEventsLocked("s", "")
			a.mu.Unlock()
			if view := upgradeProxyPage(t, events); !strings.Contains(upgradeProxyNoticeText(view), "暂时不能修改") {
				t.Fatalf("unexpected page title=%q notice=%q", view.Title, upgradeProxyNoticeText(view))
			}
			if view := upgradeProxyPage(t, readEvents); view.Title != "升级网络" {
				t.Fatal("navigation blocked")
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("busy setting changed config")
			}
		})
	}
}

func TestUpgradeProxySettingsErrorsStayVisible(t *testing.T) {
	a, path, _ := upgradeProxyFixture(t)
	before, _ := os.ReadFile(path)
	a.mu.Lock()
	events := a.upgradeProxySettingsEventsLocked("s", "bogus")
	a.mu.Unlock()
	if len(events) != 1 || events[0].Notice == nil || events[0].Notice.Code != "upgrade_proxy_settings_failed" {
		t.Fatalf("events=%#v", events)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("invalid input changed config")
	}
	a.admin.loadConfig = func() (config.LoadedAppConfig, error) {
		return config.LoadedAppConfig{}, errors.New("unreadable test config")
	}
	if _, err := a.upgradeProxyMode(); err == nil {
		t.Fatal("silently ignored config read error")
	}
}

func upgradeProxyNoticeText(view control.FeishuPageView) string {
	var lines []string
	for _, section := range view.NoticeSections {
		lines = append(lines, section.Lines...)
	}
	return strings.Join(lines, "\n")
}

func TestUpgradeProxyStaleCardCannotChangeSettings(t *testing.T) {
	a, path, _ := upgradeProxyFixture(t)
	a.service.MaterializeSurface("surface-1", "app-1", "chat-1", "user-1")
	before, _ := os.ReadFile(path)
	applyGatewayActionForTest(context.Background(), a, control.Action{
		Kind: control.ActionUpgradeCommand, GatewayID: "app-1", SurfaceSessionID: "surface-1", ChatID: "chat-1", ActorUserID: "user-1",
		MessageID: "old-card", Text: "/upgrade proxy direct",
		Inbound: &control.ActionInboundMeta{CardDaemonLifecycleID: "previous-daemon-lifecycle"},
	})
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("stale card changed upgrade proxy policy")
	}
}
