package temporalstate

import "testing"

// The exact shape of these two search attributes is the thing most likely to
// break this demo on a different server version, and neither is obvious from
// the docs, so both are pinned down here. The expected values were taken from a
// live dev server (Temporal 1.30.1).
func TestBuildIDFromVersionSA(t *testing.T) {
	const deployment = "demo/order-service"

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"documented colon form", "demo/order-service:v1-abc123", "v1-abc123"},
		{"older dot delimiter", "demo/order-service.v1-abc123", "v1-abc123"},
		{"bare build id", "v1-abc123", "v1-abc123"},
		{"absent", "", ""},
		{"build id containing a colon", "demo/order-service:v1:abc", "v1:abc"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildIDFromVersionSA(tc.input, deployment); got != tc.want {
				t.Errorf("buildIDFromVersionSA(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestNormalizeBehavior(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// What the server actually writes.
		{"Pinned", BehaviorPinned},
		{"AutoUpgrade", BehaviorAutoUpgrade},
		// Spellings the docs use elsewhere, tolerated so a server or docs change
		// does not silently empty the diagram.
		{"PINNED", BehaviorPinned},
		{"Auto-Upgrade", BehaviorAutoUpgrade},
		{"AUTO_UPGRADE", BehaviorAutoUpgrade},
		// Unversioned executions have no behavior at all.
		{"", ""},
		{"Unspecified", ""},
	}

	for _, tc := range tests {
		if got := normalizeBehavior(tc.input); got != tc.want {
			t.Errorf("normalizeBehavior(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
