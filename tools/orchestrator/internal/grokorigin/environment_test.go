//go:build !windows

package grokorigin

import (
	"strings"
	"testing"
)

func TestOriginEnvironmentNoneNeverReadsAuthentication(t *testing.T) {
	env, err := originEnvironment("none", "/private/owned/home", "/private/owned/grok", "/private/owned/tmp", func(string) (string, bool) {
		t.Fatal("none mode accessed authentication environment")
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]string{}
	for _, entry := range env {
		k, v, ok := strings.Cut(entry, "=")
		if !ok || fields[k] != "" {
			t.Fatalf("duplicate/malformed fixed env entry %q", entry)
		}
		fields[k] = v
	}
	if fields["HOME"] != "/private/owned/home" || fields["GROK_HOME"] != "/private/owned/grok" || fields["TMPDIR"] != "/private/owned/tmp" {
		t.Fatal("private layout missing")
	}
	for key := range fields {
		switch key {
		case "HOME", "GROK_HOME", "TMPDIR", "PATH", "LANG", "LC_ALL", "TERM", "NO_COLOR":
		default:
			t.Fatalf("unexpected ambient variable %s", key)
		}
	}
}
func TestOriginEnvironmentOnlyExplicitXAIKey(t *testing.T) {
	const secret = "owned-fixture-key"
	calls := 0
	env, err := originEnvironment("xai_api_key_env", "/private/home", "/private/grok", "/private/tmp", func(key string) (string, bool) {
		calls++
		if key != "XAI_API_KEY" {
			t.Fatalf("unexpected auth lookup %s", key)
		}
		return secret, true
	})
	if err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	found := 0
	for _, entry := range env {
		if entry == "XAI_API_KEY="+secret {
			found++
		}
	}
	if found != 1 {
		t.Fatal("explicit key not included once")
	}
	for _, value := range []string{"", "contains\x00nul", "contains\nnewline", " leading", "trailing ", strings.Repeat("x", 8193)} {
		t.Run("invalid_key", func(t *testing.T) {
			env, err := originEnvironment("xai_api_key_env", "/private/home", "/private/grok", "/private/tmp", func(string) (string, bool) { return value, true })
			if err == nil || env != nil || err.Error() != "grokorigin_auth_invalid" {
				t.Fatal("invalid secret accepted or diagnostic leaked data")
			}
		})
	}
	if env, err := originEnvironment("xai_api_key_env", "/private/home", "/private/grok", "/private/tmp", func(string) (string, bool) { return "", false }); err == nil || env != nil {
		t.Fatal("missing explicit key accepted")
	}
	if env, err := originEnvironment("oauth", "/private/home", "/private/grok", "/private/tmp", nil); err == nil || env != nil {
		t.Fatal("implicit auth mode accepted")
	}
}
