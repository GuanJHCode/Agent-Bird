package main

import "testing"

func TestMarketplaceCheckDoesNotReplaceAnotherSource(t *testing.T) {
	for _, tc := range []struct{ provider, body, want string }{
		{"codex", `{"marketplaces":[]}`, "missing"},
		{"codex", `{"marketplaces":[{"name":"agent-bird","marketplaceSource":{"sourceType":"local","source":"/private/release"}}]}`, "same"},
		{"codex", `{"marketplaces":[{"name":"codex-bird","marketplaceSource":{"sourceType":"local","source":"/private/old"}}]}`, "missing"},
		{"codex", `{"marketplaces":[{"name":"agent-bird","marketplaceSource":{"sourceType":"local","source":"/private/old"}}]}`, ""},
		{"codex", `{"marketplaces":[{"name":"agent-bird","marketplaceSource":{"sourceType":"git","source":"/private/release"}}]}`, ""},
		{"claude", `[]`, "missing"},
		{"claude", `[{"name":"agent-bird","source":"directory","path":"/private/release","installLocation":"/private/cache"}]`, "same"},
		{"claude", `[{"name":"agent-bird","source":"github","installLocation":"/private/release"}]`, ""},
		{"claude", `[{"name":"agent-bird","source":"directory","path":"/private/old","installLocation":"/private/release"}]`, ""},
		{"codex", `{}`, ""}, {"claude", `{} {}`, ""},
	} {
		got, err := marketplaceSourceState(tc.provider, []byte(tc.body), "/private/release")
		if tc.want == "" {
			if err == nil {
				t.Fatalf("accepted unsafe catalog %s", tc.body)
			}
		} else if err != nil || got != tc.want {
			t.Fatalf("%s: %s %v", tc.body, got, err)
		}
	}
}
