package process

import "testing"

func TestGroupQuiescentRequiresNoRunnableMembers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		listing string
		want    bool
		invalid bool
	}{
		{"absent", "12 12 S\n", true, false},
		{"zombie-anchor", "42 42 Z\n", true, false},
		{"live-descendant", "42 42 Z\n43 42 S+\n", false, false},
		{"zombie-descendant", "42 42 Z\n43 42 Z\n", true, false},
		{"malformed", "42 42\n", false, true},
		{"bad-pid", "x 42 Z\n", false, true},
		{"bad-pgid", "42 x Z\n", false, true},
		{"unknown-state", "42 42 ?\n", false, false},
		{"exiting-target-is-not-quiescent", "42 42 ?E\n", false, false},
		{"exiting-foreign-process-is-unrelated", "42 42 Z\n43 43 ?E\n", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := groupListingQuiescent([]byte(tc.listing), 42)
			if (err != nil) != tc.invalid || got != tc.want {
				t.Fatalf("quiescent=%v err=%v", got, err)
			}
		})
	}
}

func TestWaitableGroupLeaderRequiresMatchingBirthAndGroup(t *testing.T) {
	const birth = "Fri Oct  2 20:17:22 2026"
	for _, tc := range []struct {
		name      string
		listing   string
		wantMatch bool
	}{
		{"match", "42 Fri Oct  2 20:17:22 2026\n", true},
		{"wrong-group", "43 Fri Oct  2 20:17:22 2026\n", false},
		{"wrong-birth", "42 Fri Oct  2 20:17:23 2026\n", false},
		{"absent", "", false},
		{"malformed", "x Fri Oct  2 20:17:22 2026\n", false},
		{"multiple-lines", "42 Fri Oct  2 20:17:22 2026\n43 Fri Oct  2 20:17:22 2026\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := matchGroupLeaderListing([]byte(tc.listing), 42, birth)
			if (err == nil) != tc.wantMatch {
				t.Fatalf("match error=%v", err)
			}
		})
	}
}
