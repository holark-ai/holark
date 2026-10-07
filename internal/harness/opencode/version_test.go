package opencode

import "testing"

// IsV2Version selects the v2 dispatch path. Only normalized releases at or
// above the 2.0.0 boundary opt in; empty, legacy, and unparseable output keeps
// the validated v1 behavior.
func TestIsV2Version(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		want      bool
	}{
		{name: "empty", raw: "", want: false},
		{name: "validated v1", raw: "opencode v1.18.35", want: false},
		{name: "v2 release", raw: "opencode v2.0.8", want: true},
		{name: "garbage", raw: "not a version", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := IsV2Version(test.raw); got != test.want {
				t.Fatalf("IsV2Version(%q) = %v, want %v", test.raw, got, test.want)
			}
		})
	}
}
