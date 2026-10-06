package host

import (
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
)

const codexFixtureThreadID = "01992541-b000-7000-8000-111111111111"

func codexFixtureThreadBinding() codexsession.ThreadBinding {
	return codexsession.ThreadBinding{ThreadID: codexFixtureThreadID, Model: "source-default-model", ModelProvider: "openai", HistoryMode: "paginated", RolloutPath: "/private/owned/home/sessions/2026/10/05/rollout-2026-10-05T12-00-00-" + codexFixtureThreadID + ".jsonl"}
}

// Direct private helper only. No Host, ExecuteLaunch, filesystem, TestMain,
// process launcher, native binary, source config or source account access.
func TestCodexManagedBindingRequiresExactOwnedRollout(t *testing.T) {
	b := codexFixtureThreadBinding()
	relative, err := codexManagedRolloutRelative("/private/owned/home", b)
	if err != nil || relative != "2026/10/05/rollout-2026-10-05T12-00-00-"+codexFixtureThreadID+".jsonl" {
		t.Fatalf("exact managed identity unavailable: relative=%q err=%v", relative, err)
	}
}

func TestCodexManagedBindingRefusesInvalidPersistence(t *testing.T) {
	type fault struct {
		name   string
		mutate func(*codexsession.ThreadBinding)
	}
	for _, f := range []fault{
		{"empty_id", func(b *codexsession.ThreadBinding) { b.ThreadID = "" }},
		{"opaque_id", func(b *codexsession.ThreadBinding) { b.ThreadID = "thread-1" }},
		{"uppercase_id", func(b *codexsession.ThreadBinding) { b.ThreadID = strings.ToUpper(b.ThreadID) }},
		{"empty_model", func(b *codexsession.ThreadBinding) { b.Model = "" }},
		{"oversized_model", func(b *codexsession.ThreadBinding) { b.Model = strings.Repeat("x", 257) }},
		{"foreign_provider", func(b *codexsession.ThreadBinding) { b.ModelProvider = "foreign" }},
		{"legacy", func(b *codexsession.ThreadBinding) { b.HistoryMode = "legacy" }},
		{"missing_history", func(b *codexsession.ThreadBinding) { b.HistoryMode = "" }},
		{"missing_path", func(b *codexsession.ThreadBinding) { b.RolloutPath = "" }},
		{"foreign_home", func(b *codexsession.ThreadBinding) {
			b.RolloutPath = strings.Replace(b.RolloutPath, "/owned/home/", "/foreign/home/", 1)
		}},
		{"relative_path", func(b *codexsession.ThreadBinding) { b.RolloutPath = strings.TrimPrefix(b.RolloutPath, "/") }},
		{"noncanonical_path", func(b *codexsession.ThreadBinding) {
			b.RolloutPath = strings.Replace(b.RolloutPath, "/sessions/", "/sessions/../sessions/", 1)
		}},
		{"compressed", func(b *codexsession.ThreadBinding) { b.RolloutPath += ".zst" }},
		{"different_uuid", func(b *codexsession.ThreadBinding) {
			b.RolloutPath = strings.Replace(b.RolloutPath, codexFixtureThreadID, "01992541-b000-7000-8000-222222222222", 1)
		}},
		{"branch_suffix", func(b *codexsession.ThreadBinding) {
			b.RolloutPath = strings.Replace(b.RolloutPath, ".jsonl", "_"+codexFixtureThreadID+".jsonl", 1)
		}},
		{"extra_depth", func(b *codexsession.ThreadBinding) {
			b.RolloutPath = strings.Replace(b.RolloutPath, "/sessions/", "/sessions/foreign/", 1)
		}},
		{"date_drift", func(b *codexsession.ThreadBinding) {
			b.RolloutPath = strings.Replace(b.RolloutPath, "/10/05/", "/10/04/", 1)
		}},
		{"invalid_date", func(b *codexsession.ThreadBinding) {
			b.RolloutPath = strings.ReplaceAll(b.RolloutPath, "10-05", "02-30")
			b.RolloutPath = strings.Replace(b.RolloutPath, "/10/05/", "/02/30/", 1)
		}},
		{"invalid_time", func(b *codexsession.ThreadBinding) {
			b.RolloutPath = strings.Replace(b.RolloutPath, "T12-00-00", "T24-00-00", 1)
		}},
		{"invalid_utf8", func(b *codexsession.ThreadBinding) { b.RolloutPath += string([]byte{0xff}) }},
		{"nul_path", func(b *codexsession.ThreadBinding) { b.RolloutPath += "\x00" }},
	} {
		t.Run(f.name, func(t *testing.T) {
			b := codexFixtureThreadBinding()
			f.mutate(&b)
			if _, err := codexManagedRolloutRelative("/private/owned/home", b); err == nil {
				t.Fatal("invalid native identity can cross the dedicated managed binding barrier")
			}
		})
	}
	for _, home := range []string{"", "home", "/", "/private/owned/home/", "/private/owned/../owned/home", strings.Repeat("x", 4097)} {
		t.Run("home_"+string(rune('a'+len(home)%20)), func(t *testing.T) {
			if _, err := codexManagedRolloutRelative(home, codexFixtureThreadBinding()); err == nil {
				t.Fatal("noncanonical original home admitted")
			}
		})
	}
}
