package adapter

import "errors"
import "strings"

var errVersionUnparsed = errors.New("cli_version_unparsed")

// VersionView is the compatibility decision for one raw --version string.
// Admitted versions still bind path, raw text, and SHA-256 at launch.
type VersionView struct {
	Provider   Provider `json:"provider"`
	Baseline   string   `json:"baseline,omitempty"`
	Ceiling    string   `json:"ceiling,omitempty"`
	FrozenAt   string   `json:"frozen_at,omitempty"`
	Normalized string   `json:"normalized,omitempty"`
	Protocol   string   `json:"protocol,omitempty"`
	Admitted   bool     `json:"admitted"`
	Reason     string   `json:"reason,omitempty"`
}

// VersionError carries a stable code plus the frozen window.
// Error() stays the code so existing callers can compare it directly.
type VersionError struct {
	Code       string
	Provider   Provider
	Raw        string
	Baseline   string
	Ceiling    string
	Normalized string
}

func (e *VersionError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code
}

func AdmitVersion(provider Provider, raw string) error {
	_, err := assessVersion(provider, raw)
	return err
}

func DescribeVersion(provider Provider, raw string) VersionView {
	view, _ := assessVersion(provider, raw)
	return view
}

// TrialBlockedError is the Codex launch gate. Error() stays the stable code.
// Missing lists the contracts that are still absent, so the gate is not a bare string.
type TrialBlockedError struct {
	Missing []string
}

func (e *TrialBlockedError) Error() string {
	if e == nil {
		return ""
	}
	return "codex_trial_guard_not_ready"
}

func CodexTrialBlocked() error {
	return &TrialBlockedError{Missing: CodexTrialMissing()}
}

func CodexTrialMissing() []string {
	return []string{
		"verified_auth_metadata_source_missing",
		"exec_refresh_tripwire_not_applied",
		"native_auth_admission_pinned_0.154.0",
		"codex_exec_collect_loop_not_evidenced",
	}
}

// LaunchAdmission decides before any binary read. A nil result means the
// caller still has to verify the pin, digest, and capabilities.
func LaunchAdmission(provider Provider, rawVersion, sha string) error {
	if rawVersion == CodexVersion || strings.EqualFold(sha, CodexSHA256) {
		return CodexTrialBlocked()
	}
	if err := AdmitVersion(provider, rawVersion); err != nil {
		return err
	}
	if provider == ProviderCodex {
		return CodexTrialBlocked()
	}
	return nil
}

func assessVersion(provider Provider, raw string) (VersionView, error) {
	view := VersionView{Provider: provider}
	catalog, err := loadCatalog()
	if err != nil {
		return rejectVersion(view, "cli_version_catalog_invalid", raw)
	}
	policy := catalog.byProvider[provider]
	if policy == nil {
		return rejectVersion(view, "cli_version_unparsed", raw)
	}
	view.Baseline, view.Ceiling, view.FrozenAt = policy.baselineText, policy.ceilingText, catalog.frozenAt
	parsed, err := parseCLIVersion(raw)
	if err != nil {
		return rejectVersion(view, "cli_version_unparsed", raw)
	}
	if !prefixAllowed(provider, parsed.Product) {
		return rejectVersion(view, "cli_version_product_mismatch", raw)
	}
	view.Normalized = parsed.Normalized
	return classifyReleased(view, policy, parsed, raw)
}

func classifyReleased(view VersionView, policy *providerPolicy, parsed parsedVersion, raw string) (VersionView, error) {
	if _, code := releaseClass(view.Provider, parsed); code != "" {
		return rejectVersion(view, code, raw)
	}
	if parsed.compare(policy.baseline) < 0 {
		return rejectVersion(view, "cli_version_below_baseline", raw)
	}
	if parsed.compare(policy.ceiling) > 0 {
		return rejectVersion(view, "cli_version_above_ceiling", raw)
	}
	if _, ok := policy.published[parsed.Normalized]; !ok {
		return rejectVersion(view, "cli_version_not_published", raw)
	}
	view.Admitted = true
	view.Protocol = policy.protocol
	return view, nil
}

func rejectVersion(view VersionView, code, raw string) (VersionView, error) {
	view.Admitted = false
	view.Reason = code
	return view, &VersionError{
		Code: code, Provider: view.Provider, Raw: raw,
		Baseline: view.Baseline, Ceiling: view.Ceiling, Normalized: view.Normalized,
	}
}
