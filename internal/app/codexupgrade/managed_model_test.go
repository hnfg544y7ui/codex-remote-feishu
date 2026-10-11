package codexupgrade

import "testing"

func TestManagedInstallationUpgradeCapability(t *testing.T) {
	for _, tc := range []struct {
		name         string
		installation Installation
		want         bool
	}{
		{"active managed", Installation{SourceKind: SourceManagedRuntime, PackageVersion: "0.134.0"}, true},
		{"uninitialized managed", Installation{SourceKind: SourceManagedRuntime}, false},
		{"external npm", Installation{SourceKind: SourceStandaloneNPM, PackageVersion: "0.134.0"}, true},
		{"desktop bundle", Installation{SourceKind: SourceVSCodeBundle, PackageVersion: "0.134.0"}, false},
		{"unknown external", Installation{SourceKind: SourceStandaloneOther, PackageVersion: "0.134.0"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.installation.Upgradeable(); got != tc.want {
				t.Fatalf("upgrade capability = %v, want %v", got, tc.want)
			}
		})
	}
}
