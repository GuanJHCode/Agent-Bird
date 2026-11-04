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
		"native_auth_admission_exact_pin",
		"codex_exec_collect_loop_not_evidenced",
	}
}

// LaunchAdmission decides before any binary read. A nil result means the
// caller still has to verify the pin, digest, and capabilities.
// CodexNativePins is deliberately narrower than the frozen version catalog.
// Additions require native refresh-tripwire, named tool-policy and real
// read/collect/ACK/accept evidence for the exact binary.
func CodexNativePins() map[string]string {
	return map[string]string{
		"codex-cli 0.154.0": "4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc",
		"codex-cli 0.155.0": "b0b14f9c1901c1ec44671094b2dc18b39e4bd8d36a6dc2302cc9d961a7e2a197",
		"codex-cli 0.155.1": "8eaf1ad12fe6bf89b1710330f58900014322c7c5af677e43be116d8ac5fc0a9e",
		"codex-cli 0.156.0": "6b42db4d33fd53516162bd76a0e2d07e0567287c44e036d4e4c06cb555a432f9",
		"codex-cli 0.156.1": "0196e89fe5a7598f816ee54232c3d7c26d75e502ab5cfe2c9240e81d90f7255a",
		"codex-cli 0.157.0": "ad0be20d04e2ba6146ecdb51d7f8b7b0fe15420a15dc9b0057518d858f1f3714",
		"codex-cli 0.157.1": "27ceb5f9b957b43a519efe4eaa3816a0bffb0a531a2c89af18840c0a3c016a7d",
		"codex-cli 0.158.0": "788a818fbb9596869c7a487554507cb8bdca17584b8671112b23f9e225ba35c8",
		"codex-cli 0.159.0": "e89718aa1969bfc4a471277bdc4679a3a3529293de0a309909822dfd67ddb77a",
		"codex-cli 0.159.1": "ab2ed513b0ae735151799ab57045e05ca721c4730859b5aa2f2a9e9bed287eb8",
		"codex-cli 0.159.2": "16593cc2f422d5f398a8e40f550ebbaf1245392528957be342c295920a300704",
		"codex-cli 0.159.3": "4d210f7c5a18fd0386434df23b5bdbb8c0e7257d3e8a2b30b0769c8bbe99a878",
		"codex-cli 0.160.0": "112fae7a5a1223e673c8a1791d32338f37df8b527ff1159bb8adac6c4dbf1b4b",
		"codex-cli 0.160.1": "09fa44fdc37a5fc70dc1ace31235f90468a2e193d0e85f7552eab068ea2582be",
		"codex-cli 0.161.0": "12ac11d2c7eee27cfae34393986d7b7c9ed0dea537cb749831cdd7033893e6de",
		"codex-cli 0.162.0": "8238273076041e55beded03ba1d51e034f997b7d100b0642d36daf036620fe57",
		"codex-cli 0.162.1": "74c6a6d263f40d65f99c27a815275f5b4ed0bcc2baecc396b4cf3255498bdfc0",
		"codex-cli 0.163.0": "8813367285524ac798dd96d1b071aea30d58708c7226fd236cd275cf1cfdee31",
		"codex-cli 0.164.0": "d8990e3ee7acfbc26f91265b12a1a83f0fee40d0e0f8f1f40a37d4ec21f499cf",
		"codex-cli 0.165.0": "a45099154d7fb4a5ed8a56f40966a95deb938e508bab23e6985035c709e9a85a",
		"codex-cli 0.166.0": "39acf979729dd290f9a24ee38b2914e036c91b65952872e28ae2a6b88a7db7c1",
	}
}

func LaunchAdmission(provider Provider, rawVersion, sha string) error {
	if provider != ProviderCodex {
		for raw, digest := range CodexNativePins() {
			if rawVersion == raw || strings.EqualFold(sha, digest) {
				return CodexTrialBlocked()
			}
		}
	}
	if err := AdmitVersion(provider, rawVersion); err != nil {
		return err
	}
	if provider == ProviderCodex {
		want, ok := CodexNativePins()[rawVersion]
		if !ok || !strings.EqualFold(sha, want) {
			return CodexTrialBlocked()
		}
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

// Write contracts are measured independently of the read-only version window.
// These exact artifacts passed native managed-write permission/transport tests;
// admitting another read-only release does not grant it coding capability.
func CodexWorkspaceWriteAdmission(rawVersion, sha string) error {
	if rawVersion != "codex-cli 0.166.0" || !strings.EqualFold(sha, CodexNativePins()[rawVersion]) {
		return errors.New("codex_workspace_write_unverified")
	}
	return nil
}

func ClaudeGatewayWorkspaceWriteAdmission(rawVersion, sha string) error {
	if err := AdmitVersion(ProviderClaude, rawVersion); err != nil {
		return err
	}
	version, err := parseCLIVersion(rawVersion)
	if err != nil {
		return err
	}
	for raw, digest := range map[string]string{
		"2.1.267": "a681f3008f0050029aeebcab3af51bb6a55ddeb625a3af3141a4416d43cd2558",
		"2.1.316": "64c327e92b2c5e0f78c0a980c5a518121e1f0d140ca771ca5dd4488d1a2dcb3f",
	} {
		measured, _ := parseCLIVersion(raw)
		if version.compare(measured) == 0 && strings.EqualFold(sha, digest) {
			return nil
		}
	}
	return errors.New("claude_gateway_workspace_write_unverified")
}
