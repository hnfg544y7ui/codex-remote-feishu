package upgradecontract

import "testing"

func TestUpgradeProxyCommandsAndPresentation(t *testing.T) {
	for _, tc := range []struct {
		text         string
		mode         CommandMode
		proxy        string
		presentation Presentation
	}{
		{"/upgrade proxy", CommandShowProxy, "", PresentationPage},
		{"/upgrade proxy system", CommandSetProxy, "system", PresentationExecute},
		{"/upgrade proxy direct", CommandSetProxy, "direct", PresentationExecute},
	} {
		parsed, err := ParseCommandText(tc.text)
		if err != nil || parsed.Mode != tc.mode || parsed.ProxyMode != tc.proxy {
			t.Fatalf("%s => %#v, %v", tc.text, parsed, err)
		}
		if got, ok := PresentationForText(tc.text); !ok || got != tc.presentation {
			t.Fatalf("%s presentation = %q %v", tc.text, got, ok)
		}
	}
	for _, text := range []string{"/upgrade proxy auto", "/upgrade proxy direct extra"} {
		if _, err := ParseCommandText(text); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	def := BuildDefinition(CapabilityPolicy{})
	for _, option := range def.Options {
		if option.CommandText == "/upgrade proxy" && option.Label == "升级网络" {
			if arg, ok := NormalizeMenuArgument(option.Value); !ok || arg != "proxy" {
				t.Fatal("proxy menu argument rejected")
			}
			return
		}
	}
	t.Fatal("upgrade network entry missing")
}

func TestNormalizeUpgradeProxyMenuChoices(t *testing.T) {
	for _, mode := range []string{"system", "direct"} {
		for _, prefix := range []string{"proxy ", "proxy_", "proxy-"} {
			if got, ok := NormalizeMenuArgument(prefix + mode); !ok || got != "proxy "+mode {
				t.Fatalf("rejected choice %q", prefix+mode)
			}
		}
	}
	if _, ok := NormalizeMenuArgument("proxy bogus"); ok {
		t.Fatal("invalid choice accepted")
	}
}
