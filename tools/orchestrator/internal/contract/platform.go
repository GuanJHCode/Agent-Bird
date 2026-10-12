package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// PlatformContract is a build-time interface description, never an execution
// grant or a probe of the current machine, provider, installation or database.
// Consumers must reject unknown versions and gates and still perform admission.
type PlatformContract struct {
	Version      int                   `json:"version"`
	OS           string                `json:"os"`
	Arch         string                `json:"arch"`
	Capabilities []PlatformCapability  `json:"capabilities"`
	Events       PlatformEventContract `json:"events"`
}

type PlatformCapability struct {
	ID       string   `json:"id"`
	Version  int      `json:"version"`
	Gate     string   `json:"gate"` // public, internal, blocked
	Reason   string   `json:"reason"`
	Requires []string `json:"requires"`
}

type PlatformEventContract struct {
	Version       int      `json:"version"`
	Kinds         []string `json:"kinds"`
	Authority     string   `json:"authority"`
	TerminalProof string   `json:"terminal_proof"`
}

// CanonicalJSON uses this fixed field order, ordered slices, encoding/json's
// escaping and no trailing newline. The hash is domain separated; it is an
// integrity identifier, not a signature or authority to execute an operation.
func (p PlatformContract) CanonicalJSON() ([]byte, string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, "", err
	}
	h := sha256.Sum256(append([]byte("agent-bird/platform-contract/v1\n"), b...))
	return b, hex.EncodeToString(h[:]), nil
}

func CompiledPlatformContract(goos, goarch string) PlatformContract {
	p := PlatformContract{Version: 1, OS: goos, Arch: goarch,
		Events: PlatformEventContract{Version: 1,
			Kinds:         []string{EventPrepared, EventSpawned, EventRunning, EventSession, EventProgress, EventResult, EventQuestion, EventFailed, EventStopped, EventExited, EventLaunchRejected, EventUnknown},
			Authority:     "authenticated_producer_sequence_hash_epoch_durable_ack",
			TerminalProof: "original_wait_and_owned_tree_exit"},
	}
	add := func(id, gate, reason string, requires ...string) {
		if requires == nil {
			requires = []string{}
		}
		p.Capabilities = append(p.Capabilities, PlatformCapability{ID: id, Version: 1, Gate: gate, Reason: reason, Requires: requires})
	}
	darwinGate, darwinReason := "internal", "darwin_private_primitive"
	if goos != "darwin" {
		darwinGate, darwinReason = "blocked", "darwin_required"
	}
	add("codex_worker", "blocked", "codex_execution_domain_not_verified", "pinned_provider", "separate_tool_network_and_state_boundary", "native_read_write_continuation")
	add("filtered_broker_carrier", darwinGate, darwinReason, "original_host", "bound_manifest", "exact_transform_permit", "original_wait")
	add("filtered_conversion_freeze", darwinGate, darwinReason, "broker", "raw_canonical_roundtrip", "frozen_input", "original_wait")
	for _, id := range []string{"filtered_compose", "filtered_rework", "filtered_retry", "filtered_continuation"} {
		add(id, "blocked", "shared_action_contract_not_implemented")
	}
	for _, id := range []string{"git_lfs", "git_custom_hooks", "git_signing"} {
		add(id, "blocked", "extension_execution_contract_not_qualified")
	}
	supervisionGate := "public"
	supervisionReason := "new_supervised_tasks_only"
	if goos != "darwin" && goos != "linux" {
		supervisionGate, supervisionReason = "blocked", "platform_unsupported"
	}
	add("original_owner_supervision", supervisionGate, supervisionReason,
		"supervised_v1", "original_owner_alive", "signed_current_scope", "exact_pid_birth", "original_handle_retained",
		"current_origin_control_revision_budget_admission")
	add("owner_prebind_recovery_v2", supervisionGate, supervisionReason,
		"owner_launch_protocol_2", "same_scope", "original_supervisor_wait", "first_host_exited", "at_most_one_replacement",
		"current_origin_control_revision_budget_admission")
	add("historical_wait_reconstruction", "blocked", "lost_original_wait_is_not_reconstructible")
	add("unknown_execution_takeover", "blocked", "missing_original_owner_proof")
	return p
}
