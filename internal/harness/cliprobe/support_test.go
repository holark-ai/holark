package cliprobe

import (
	"errors"
	"strings"
	"testing"
)

// AtLeastVersion gates the OpenCode v2 dispatch boundary (2.0.0). Releases
// below the boundary and anything unparseable keep the validated v1 path; a
// prerelease ranks below its release per semver.
func TestAtLeastVersionV2Boundary(t *testing.T) {
	for _, test := range []struct {
		name, value string
		want        bool
	}{
		{name: "validated v1", value: "1.18.25", want: false},
		{name: "v1 late minor", value: "1.99.99", want: false},
		{name: "boundary", value: "2.0.0", want: true},
		{name: "above boundary", value: "2.0.1", want: true},
		{name: "garbage", value: "not a version", want: false},
		{name: "empty", value: "", want: false},
		{name: "boundary prerelease", value: "2.0.0-alpha", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := AtLeastVersion(test.value, 2, 0, 0); got != test.want {
				t.Fatalf("AtLeastVersion(%q, 2, 0, 0) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

// Arbitrary version outputs cannot reliably be exercised with installed CLIs.
// These cases cover production classification without emulating an executable.
func TestClassifyInstalledVersion(t *testing.T) {
	policy := SupportPolicy{Versions: []string{"1.2.3"}, Latest: "1.2.3"}
	for _, test := range []struct {
		name, output, status          string
		versionError, resolutionError error
	}{
		{name: "supported", output: "1.2.3", status: "supported"},
		{name: "build metadata", output: "1.2.3+build.7", status: "supported"},
		{name: "too old", output: "1.2.2", status: "unsupported"},
		{name: "too new", output: "1.2.4", status: "unsupported"},
		{name: "prerelease", output: "1.2.3-beta.1", status: "unsupported"},
		{name: "unrecognized", output: "development", status: "unknown"},
		{name: "incomplete", output: "1.2", status: "unknown"},
		{name: "invalid prerelease", output: "1.2.3-beta.01", status: "unknown"},
		{name: "oversized", output: strings.Repeat("a", 200), status: "unknown"},
		{name: "failed command with output", output: "1.2.3", status: "unknown", versionError: errors.New("exit 1")},
		{name: "failed command without output", status: "unknown", versionError: errors.New("timeout")},
		{name: "missing", resolutionError: errors.New("not found")},
	} {
		t.Run(test.name, func(t *testing.T) {
			observation := Observation{Path: "/installed/agent", Output: test.output, VersionError: test.versionError, ResolutionError: test.resolutionError}
			got := Classify(observation, "Agent", "Agent was not found", policy, func(value string) string { return value })
			if got.Available != (test.resolutionError == nil) || got.SupportStatus != test.status {
				t.Fatalf("classification = %+v", got)
			}
			if got.RawVersion != test.output || got.Path != observation.Path {
				t.Fatalf("discovery metadata lost: %+v", got)
			}
			if got.Available && got.Reason != "" {
				t.Fatalf("version became an unavailable reason: %+v", got)
			}
			if test.status == "unsupported" || test.status == "unknown" {
				if !strings.Contains(got.Warning, "Holark supports Agent <= 1.2.3.") || !strings.Contains(got.Warning, "may not work as expected") {
					t.Fatalf("missing supported target: %+v", got)
				}
			} else if got.Warning != "" {
				t.Fatalf("unexpected warning: %+v", got)
			}
			if test.status == "unknown" && (got.ParsedVersion != nil || !strings.Contains(got.Warning, "could not be identified") && !strings.Contains(got.Warning, "Version check failed")) {
				t.Fatalf("unknown version misclassified: %+v", got)
			}
			if test.name == "prerelease" && (got.Version != test.output || got.ParsedVersion.Prerelease != "beta.1") {
				t.Fatalf("prerelease lost: %+v", got)
			}
		})
	}
}
