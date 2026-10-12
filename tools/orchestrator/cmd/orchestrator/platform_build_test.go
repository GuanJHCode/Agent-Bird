package main

import (
	"strings"
	"testing"
)

func TestPlatformBuildIdentityRequiresExplicitCleanProductPin(t *testing.T) {
	for _, tt := range []struct {
		sha, clean, want string
		modified         bool
	}{
		{"", "", "unknown", true},
		{strings.Repeat("a", 40), "true", strings.Repeat("a", 40), false},
		{strings.Repeat("a", 40), "false", strings.Repeat("a", 40), true},
		{strings.Repeat("a", 40), "", "unknown", true},
		{strings.Repeat("A", 40), "true", "unknown", true},
		{strings.Repeat("a", 39), "true", "unknown", true},
		{"outer_repo", "true", "unknown", true},
	} {
		sha, modified := platformSourceIdentity(tt.sha, tt.clean)
		if sha != tt.want || modified != tt.modified {
			t.Fatalf("%q/%q: %q/%v", tt.sha, tt.clean, sha, modified)
		}
	}
}
