package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"
)

type launchFailureDiagnostic struct {
	Version   int    `json:"version"`
	Kind      string `json:"kind"`
	Stage     string `json:"stage"`
	CauseCode string `json:"cause_code"`
}

// safeLaunchFailureCauseCodes maps a whole error string to the cause code that
// may be recorded verbatim in a durable launch diagnostic. Every entry is a
// fixed literal chosen by this package or by the adapter's version/pin gates, so
// none of them can carry a path, a provider message, a decoder field name, or a
// credential.
//
// Membership here does not weaken any guard. The launch is still refused exactly
// as before; only the reason becomes attributable instead of collapsing into the
// opaque "launch_failed". That attribution is what makes binary pin drift, a
// frozen-window rejection, or a path-trust rejection distinguishable per run.
var safeLaunchFailureCauseCodes = map[string]string{
	// Payload and candidate source contracts.
	"invalid_json":                         "invocation_payload_invalid",
	"candidate_validation_source_invalid":  "candidate_validation_source_invalid",
	"candidate_review_source_invalid":      "candidate_review_source_invalid",
	"candidate_integration_source_invalid": "candidate_integration_source_invalid",
	"candidate_input_changed":              "candidate_input_changed",
	"candidate_target_mismatch":            "candidate_target_mismatch",
	"invocation_unavailable":               "invocation_unavailable",
	// Path and workspace trust.
	"profile_workspace_untrusted":      "profile_workspace_untrusted",
	"report_capability_path_untrusted": "report_capability_path_untrusted",
	"profile_role_invalid":             "profile_role_invalid",
	"isolated_worktree_required":       "isolated_worktree_required",
	// Binary pin and version drift.
	"binary_path_invalid":         "binary_path_invalid",
	"binary_pin_invalid":          "binary_pin_invalid",
	"binary_version_mismatch":     "binary_version_mismatch",
	"binary_sha256_mismatch":      "binary_sha256_mismatch",
	"binary_path_changed":         "binary_path_changed",
	"binary_file_unsafe":          "binary_file_unsafe",
	"binary_read_failed":          "binary_read_failed",
	"binary_version_probe_failed": "binary_version_probe_failed",
	// Capability and trial gates.
	"provider_capability_unsupported": "provider_capability_unsupported",
	"codex_trial_guard_not_ready":     "codex_trial_guard_not_ready",
	// Frozen compatibility window.
	"cli_version_unparsed":          "cli_version_unparsed",
	"cli_version_catalog_invalid":   "cli_version_catalog_invalid",
	"cli_version_product_mismatch":  "cli_version_product_mismatch",
	"cli_version_prerelease":        "cli_version_prerelease",
	"cli_version_unrecognized_note": "cli_version_unrecognized_note",
	"cli_version_below_baseline":    "cli_version_below_baseline",
	"cli_version_above_ceiling":     "cli_version_above_ceiling",
	"cli_version_not_published":     "cli_version_not_published",
}

// recordedLaunchFailureCodes is the closed set of cause codes that may appear in
// a durable diagnostic. It is derived from the same table that produces them, so
// the writer and the grant-ledger recovery reader cannot drift apart.
var recordedLaunchFailureCodes = func() map[string]struct{} {
	codes := map[string]struct{}{"launch_failed": {}}
	for _, code := range safeLaunchFailureCauseCodes {
		codes[code] = struct{}{}
	}
	return codes
}()

// Only grant-ledger recovery may reuse a pre-event durable diagnostic.
type recoveredLaunchFailure struct{}

func (recoveredLaunchFailure) Error() string { return "recovered_unspawned_grant" }

func readRecoveredLaunchFailure(path string) ([]byte, launchFailureCause, error) {
	invalid := errors.New("launch_failure_diagnostic_invalid")
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	if err != nil {
		return nil, "", invalid
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() < 1 || info.Size() > 256 {
		return nil, "", invalid
	}
	identity, ok := info.Sys().(*syscall.Stat_t)
	if !ok || identity.Uid != uint32(os.Geteuid()) {
		return nil, "", invalid
	}
	body, err := io.ReadAll(io.LimitReader(file, 257))
	if err != nil || int64(len(body)) != info.Size() {
		return nil, "", invalid
	}
	var diagnostic launchFailureDiagnostic
	if json.Unmarshal(body, &diagnostic) != nil || diagnostic.Version != 1 || diagnostic.Kind != "launch_failure" || diagnostic.Stage != "prelaunch" {
		return nil, "", invalid
	}
	if _, ok := recordedLaunchFailureCodes[diagnostic.CauseCode]; !ok {
		return nil, "", invalid
	}
	// Exact canonical bytes reject unknown/duplicate fields and trailing content;
	// no unrecognized bytes can enter a collected artifact during recovery.
	canonical, err := json.Marshal(diagnostic)
	if err != nil || !bytes.Equal(canonical, body) {
		return nil, "", invalid
	}
	return body, launchFailureCause(diagnostic.CauseCode), nil
}

// Only this private type may carry a pre-sanitized cause into launch metadata.
// Unknown/process paths continue to use their existing classification.
type launchFailureCause string

func (e launchFailureCause) Error() string { return string(e) }

func safeLaunchFailureCause(cause error) launchFailureCause {
	if cause == nil {
		return "launch_failed"
	}
	// Deliberately match the entire error. Do not unwrap, trim, parse prefixes,
	// echo decoder field names, or copy paths/Provider error text into artifacts.
	if code, ok := safeLaunchFailureCauseCodes[cause.Error()]; ok {
		return launchFailureCause(code)
	}
	return "launch_failed"
}
