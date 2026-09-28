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
	switch diagnostic.CauseCode {
	case "launch_failed", "invocation_payload_invalid", "candidate_validation_source_invalid", "candidate_review_source_invalid", "candidate_integration_source_invalid", "candidate_input_changed", "candidate_target_mismatch", "invocation_unavailable", "profile_workspace_untrusted":
	default:
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
	switch cause.Error() {
	case "profile_workspace_untrusted":
		return "profile_workspace_untrusted"
	case "invalid_json":
		return "invocation_payload_invalid"
	case "candidate_validation_source_invalid":
		return "candidate_validation_source_invalid"
	case "candidate_review_source_invalid":
		return "candidate_review_source_invalid"
	case "candidate_integration_source_invalid":
		return "candidate_integration_source_invalid"
	case "candidate_input_changed":
		return "candidate_input_changed"
	case "candidate_target_mismatch":
		return "candidate_target_mismatch"
	case "invocation_unavailable":
		return "invocation_unavailable"
	default:
		return "launch_failed"
	}
}
