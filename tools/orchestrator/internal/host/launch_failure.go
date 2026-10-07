package host

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
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

func decodeRecoveredLaunchFailure(body []byte) (launchFailureCause, error) {
	invalid := errors.New("launch_failure_diagnostic_invalid")
	if len(body) < 1 || len(body) > 256 {
		return "", invalid
	}
	var diagnostic launchFailureDiagnostic
	if json.Unmarshal(body, &diagnostic) != nil || diagnostic.Version != 1 || diagnostic.Kind != "launch_failure" || diagnostic.Stage != "prelaunch" {
		return "", invalid
	}
	switch diagnostic.CauseCode {
	case "probe_start_eagain", "probe_start_eintr", "launch_failed", "invocation_payload_invalid", "candidate_validation_source_invalid", "candidate_review_source_invalid", "candidate_integration_source_invalid", "candidate_input_changed", "candidate_target_mismatch", "invocation_unavailable", "profile_workspace_untrusted":
	default:
		return "", invalid
	}
	// Exact canonical bytes reject unknown/duplicate fields and trailing content;
	// no unrecognized bytes can enter a collected artifact during recovery.
	canonical, err := json.Marshal(diagnostic)
	if err != nil || !bytes.Equal(canonical, body) {
		return "", invalid
	}
	return launchFailureCause(diagnostic.CauseCode), nil
}

// Only this private type may carry a pre-sanitized cause into launch metadata.
// Unknown/process paths continue to use their existing classification.
type launchFailureCause string

func (e launchFailureCause) Error() string { return string(e) }

func launchFailureFacts(cause launchFailureCause) *contract.FailureFacts {
	if cause != "probe_start_eagain" && cause != "probe_start_eintr" {
		return nil
	}
	return &contract.FailureFacts{Version: 1, Stage: "prelaunch_probe", Code: string(cause), WorkerState: "not_started", ProbeState: "not_started"}
}
func safeLaunchFailureCause(cause error) launchFailureCause {
	if facts := adapter.ProbeFailureFacts(cause); facts.ValidPrelaunchProbe() {
		return launchFailureCause(facts.Code)
	}
	if recovered, ok := cause.(launchFailureCause); ok && launchFailureFacts(recovered) != nil {
		return recovered
	}
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
