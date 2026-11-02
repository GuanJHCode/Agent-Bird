package adapter

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// The Codex gate is a contract list, not a mood. Closing an item requires
// real evidence against that contract; until then the full list must come
// back verbatim, so an implementation cannot graduate by quietly dropping
// the items it has not satisfied.
func TestCodexTrialGuardReportsTheFullMissingList(t *testing.T) {
	want := []string{
		"verified_auth_metadata_source_missing",
		"exec_refresh_tripwire_not_applied",
		"native_auth_admission_pinned_0.154.0",
		"codex_exec_collect_loop_not_evidenced",
	}
	if got := CodexTrialMissing(); !slices.Equal(got, want) {
		t.Fatalf("missing = %v, want the full contract list %v", got, want)
	}
	blocked, ok := CodexTrialBlocked().(*TrialBlockedError)
	if !ok || blocked == nil || blocked.Error() != "codex_trial_guard_not_ready" {
		t.Fatalf("guard = %#v, want a TrialBlockedError coded codex_trial_guard_not_ready", CodexTrialBlocked())
	}
	if !slices.Equal(blocked.Missing, want) {
		t.Fatalf("blocked missing = %v, want %v", blocked.Missing, want)
	}
	// Every Codex launch is gated, across the whole admitted window: neither
	// the pinned baseline nor a later admitted version slips past, and the
	// historical pin cannot alias a different provider past the gate either.
	for _, admission := range []struct {
		provider Provider
		raw, sha string
	}{
		{ProviderCodex, CodexVersion, CodexSHA256},
		{ProviderCodex, "codex-cli 0.166.0", strings.Repeat("a", 64)},
		{ProviderClaude, CodexVersion, CodexSHA256},
		{ProviderClaude, "2.1.267 (Claude Code)", CodexSHA256},
	} {
		err := LaunchAdmission(admission.provider, admission.raw, admission.sha)
		var trial *TrialBlockedError
		if !errors.As(err, &trial) || trial == nil || !slices.Equal(trial.Missing, want) {
			t.Fatalf("LaunchAdmission(%s, %q) = %v, want the full trial guard", admission.provider, admission.raw, err)
		}
	}
	// The guard is Codex-scoped: an admitted Claude version still launches.
	if err := LaunchAdmission(ProviderClaude, "2.1.267 (Claude Code)", strings.Repeat("b", 64)); err != nil {
		t.Fatalf("admitted Claude version gated: %v", err)
	}
}
