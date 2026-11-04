package adapter

import (
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"
)

var frozenCodexNativeSeries = []string{
	"codex-cli 0.154.0",
	"codex-cli 0.155.0",
	"codex-cli 0.155.1",
	"codex-cli 0.156.0",
	"codex-cli 0.156.1",
	"codex-cli 0.157.0",
	"codex-cli 0.157.1",
	"codex-cli 0.158.0",
	"codex-cli 0.159.0",
	"codex-cli 0.159.1",
	"codex-cli 0.159.2",
	"codex-cli 0.159.3",
	"codex-cli 0.160.0",
	"codex-cli 0.160.1",
	"codex-cli 0.161.0",
	"codex-cli 0.162.0",
	"codex-cli 0.162.1",
	"codex-cli 0.163.0",
	"codex-cli 0.164.0",
	"codex-cli 0.165.0",
	"codex-cli 0.166.0",
}

var frozenCodexTrialMissing = []string{
	"verified_auth_metadata_source_missing",
	"exec_refresh_tripwire_not_applied",
	"native_auth_admission_exact_pin",
	"codex_exec_collect_loop_not_evidenced",
}

func TestCodexNativePinsExactlyCoverFrozenPublishedSeries(t *testing.T) {
	wantPublished := make([]string, 0, len(frozenCodexNativeSeries))
	wantPins := make(map[string]struct{}, len(frozenCodexNativeSeries))
	for _, raw := range frozenCodexNativeSeries {
		wantPublished = append(wantPublished, strings.TrimPrefix(raw, "codex-cli "))
		wantPins[raw] = struct{}{}
	}
	if got := PublishedVersions(ProviderCodex); !slices.Equal(got, wantPublished) {
		t.Fatalf("Codex catalog releases = %v, want frozen native series %v", got, wantPublished)
	}

	pins := CodexNativePins()
	missing := make([]string, 0)
	extra := make([]string, 0)
	for raw := range wantPins {
		if _, ok := pins[raw]; !ok {
			missing = append(missing, raw)
		}
	}
	for raw := range pins {
		if _, ok := wantPins[raw]; !ok {
			extra = append(extra, raw)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) != 0 || len(extra) != 0 {
		t.Fatalf("CodexNativePins key mismatch: missing=%v extra=%v", missing, extra)
	}
}

func TestEveryCodexNativePinRetainsExactIdentityAndAntiAliasGuard(t *testing.T) {
	for raw, sha := range CodexNativePins() {
		raw, sha := raw, sha
		t.Run(strings.TrimPrefix(raw, "codex-cli "), func(t *testing.T) {
			if err := LaunchAdmission(ProviderCodex, raw, sha); err != nil {
				t.Fatalf("exact pin rejected: %v", err)
			}

			badSHA := strings.Repeat("0", 64)
			if strings.EqualFold(badSHA, sha) {
				badSHA = strings.Repeat("f", 64)
			}
			requireFullCodexTrialGuard(t, LaunchAdmission(ProviderCodex, raw, badSHA))
			requireFullCodexTrialGuard(t, LaunchAdmission(
				ProviderCodex,
				strings.TrimPrefix(raw, "codex-cli "),
				sha,
			))
			requireFullCodexTrialGuard(t, LaunchAdmission(
				ProviderClaude,
				"2.1.267 (Claude Code)",
				sha,
			))
		})
	}
}

func requireFullCodexTrialGuard(t *testing.T, err error) {
	t.Helper()
	var blocked *TrialBlockedError
	if !errors.As(err, &blocked) || blocked == nil {
		t.Fatalf("error = %v, want TrialBlockedError", err)
	}
	if !slices.Equal(blocked.Missing, frozenCodexTrialMissing) {
		t.Fatalf("missing = %v, want complete guard %v", blocked.Missing, frozenCodexTrialMissing)
	}
}
