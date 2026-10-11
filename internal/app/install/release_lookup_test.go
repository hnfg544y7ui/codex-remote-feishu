package install

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveLatestReleaseFiltersTrack(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
  {"tag_name":"v1.2.0-alpha.2","draft":false,"prerelease":true},
  {"tag_name":"v1.2.0-beta.3","draft":false,"prerelease":true},
  {"tag_name":"v1.1.0","draft":false,"prerelease":false}
]`))
	}))
	defer server.Close()

	tests := []struct {
		name  string
		track ReleaseTrack
		want  string
	}{
		{name: "production", track: ReleaseTrackProduction, want: "v1.1.0"},
		{name: "beta", track: ReleaseTrackBeta, want: "v1.2.0-beta.3"},
		{name: "alpha", track: ReleaseTrackAlpha, want: "v1.2.0-alpha.2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			release, err := ResolveLatestRelease(context.Background(), ReleaseLookupOptions{
				ReleasesAPIURL: server.URL,
				Track:          tt.track,
			})
			if err != nil {
				t.Fatalf("ResolveLatestRelease(%s) error = %v", tt.track, err)
			}
			if release.TagName != tt.want {
				t.Fatalf("ResolveLatestRelease(%s) tag = %q, want %q", tt.track, release.TagName, tt.want)
			}
		})
	}
}

func TestResolveLatestReleaseSupportsVersionReset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
  {"tag_name":"v1.0.0.1.2","draft":false,"prerelease":false},
  {"tag_name":"v1.0.0.2","draft":true,"prerelease":false},
  {"tag_name":"v1.0.0.1-alpha.2","draft":false,"prerelease":true},
  {"tag_name":"v1.0.0.1-beta.3","draft":false,"prerelease":true},
  {"tag_name":"v1.0.0.1","draft":false,"prerelease":false},
  {"tag_name":"v2.1.2","draft":false,"prerelease":false}
]`))
	}))
	defer server.Close()
	for _, tt := range []struct {
		track ReleaseTrack
		want  string
	}{
		{ReleaseTrackProduction, "v1.0.0.1"},
		{ReleaseTrackBeta, "v1.0.0.1-beta.3"},
		{ReleaseTrackAlpha, "v1.0.0.1-alpha.2"},
	} {
		t.Run(string(tt.track), func(t *testing.T) {
			release, err := ResolveLatestRelease(context.Background(), ReleaseLookupOptions{
				ReleasesAPIURL: server.URL,
				Track:          tt.track,
			})
			if err != nil {
				t.Fatal(err)
			}
			if release.TagName != tt.want {
				t.Fatalf("tag = %q, want %q", release.TagName, tt.want)
			}
		})
	}
}
