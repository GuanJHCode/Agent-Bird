package host

import "testing"

func TestCodexLoginStatusRequiresExactManagedMethod(t *testing.T) {
	if kind, err := codexLoginMethod("Logged in using ChatGPT\n", 0); err != nil || kind != "managed_chatgpt" {
		t.Fatalf("known login rejected: %v", err)
	}
	for _, out := range []string{"", "Logged in using an API key - fixture", "Logged in using ChatGPT access token", "Logged in using ChatGPT\nLogged in using ChatGPT", "prefix Logged in using ChatGPT"} {
		if _, err := codexLoginMethod(out, 0); err == nil {
			t.Fatal("unknown/ambiguous status accepted")
		}
	}
	if _, err := codexLoginMethod("Logged in using ChatGPT", 1); err == nil {
		t.Fatal("failed native command accepted")
	}
}

func TestCodexLoginAcceptsOnlyKnownReadonlyAliasWarning(t *testing.T) {
	output := "WARNING: proceeding, even though we could not update PATH: Operation not permitted (os error 1)\nLogged in using ChatGPT\n"
	if kind, err := codexLoginMethod(output, 0); err != nil || kind != "managed_chatgpt" {
		t.Fatal("known read-only alias warning rejected")
	}
	if _, err := codexLoginMethod("WARNING: unknown authentication override\nLogged in using ChatGPT", 0); err == nil {
		t.Fatal("unknown warning accepted")
	}
}

func TestCodexPinnedNativeAliasWarningsDoNotChangeAuthMethod(t *testing.T) {
	for _, prefix := range []string{"WARNING: proceeding, even though we could not create PATH aliases", "WARNING: failed to clean up stale arg0 temp dirs"} {
		if kind, err := codexLoginMethod(prefix+": Operation not permitted (os error 1)\nLogged in using ChatGPT\n", 0); err != nil || kind != "managed_chatgpt" {
			t.Fatal("pinned native read-only warning rejected")
		}
	}
}

func TestCodexAliasFailureDetailIsNotAuthenticationEvidence(t *testing.T) {
	warning := "WARNING: proceeding, even though we could not create PATH aliases: task runtime alias unavailable"
	if kind, err := codexLoginMethod(warning+"\nLogged in using ChatGPT\n", 0); err != nil || kind != "managed_chatgpt" {
		t.Fatal("native alias failure detail changed recognized auth method")
	}
	for _, out := range []string{warning + " Logged in using ChatGPT", warning + "\nLogged in using an API key - fixture", "WARNING: unknown auth mode\nLogged in using ChatGPT"} {
		if _, err := codexLoginMethod(out, 0); err == nil {
			t.Fatal("warning or another method was accepted as managed auth")
		}
	}
}
