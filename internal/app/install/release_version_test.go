package install

import "testing"

func TestReleaseVersionFormats(t *testing.T) {
	for _, tt := range []struct {
		version string
		track   string
	}{
		{"v2.1.2", "production"},
		{"v1.0.0.1", "production"},
		{"v1.0.0.12", "production"},
		{"v1.0.0.1-beta.2", "beta"},
		{"v1.0.0.1-alpha.3", "alpha"},
		{"v2.1.2-beta.2", "beta"},
		{"v1.0.0.1.2", ""},
		{"v1.0.0.", ""},
		{"v1.0.0.x", ""},
		{"dev-latest", ""},
	} {
		t.Run(tt.version, func(t *testing.T) {
			if got := looksLikeReleaseVersion(tt.version); got != (tt.track != "") {
				t.Fatalf("looksLikeReleaseVersion = %v, want %v", got, tt.track != "")
			}
			if got := trackFromVersion(tt.version); got != tt.track {
				t.Fatalf("trackFromVersion = %q, want %q", got, tt.track)
			}
		})
	}
}
