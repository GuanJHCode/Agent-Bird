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
		"native_auth_admission_exact_pin",
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
	// A version catalog entry alone never admits a native identity. Unknown
	// digests and aliases retain the complete unresolved contract list.
	for _, admission := range []struct {
		provider Provider
		raw, sha string
	}{
		{ProviderCodex, CodexVersion, strings.Repeat("0", 64)},
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
	// Only fully evidenced exact native identities may reach the runtime guard.
	for raw, sha := range CodexNativePins() {
		if err := LaunchAdmission(ProviderCodex, raw, sha); err != nil {
			t.Fatalf("evidenced native pin blocked: %v", err)
		}
	}

	// The guard is Codex-scoped: an admitted Claude version still launches.
	if err := LaunchAdmission(ProviderClaude, "2.1.267 (Claude Code)", strings.Repeat("b", 64)); err != nil {
		t.Fatalf("admitted Claude version gated: %v", err)
	}
}

func TestCodexCatalogRejectionIsSeparateFromUnverifiedNativeGate(t *testing.T) {
	for _, test := range []struct{ raw, code string }{
		{"invalid", "cli_version_unparsed"},
		{"codex-cli 0.153.4", "cli_version_below_baseline"},
		{"codex-cli 0.167.0", "cli_version_above_ceiling"},
	} {
		err := LaunchAdmission(ProviderCodex, test.raw, strings.Repeat("c", 64))
		var rejected *VersionError
		if !errors.As(err, &rejected) || rejected.Code != test.code {
			t.Fatalf("catalog rejection %s: %v", test.raw, err)
		}
	}
	for _, raw := range []string{"codex-cli 0.160.0", "codex-cli 0.166.0"} {
		err := LaunchAdmission(ProviderCodex, raw, strings.Repeat("c", 64))
		var blocked *TrialBlockedError
		if !errors.As(err, &blocked) || !slices.Equal(blocked.Missing, CodexTrialMissing()) {
			t.Fatalf("unverified native %s: %v", raw, err)
		}
	}
}
