package gitops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
)

type CompositionManifest struct {
	Version               int                 `json:"version"`
	RunID                 string              `json:"run_id"`
	TaskID                string              `json:"task_id"`
	AttemptID             string              `json:"attempt_id"`
	SegmentID             string              `json:"segment_id"`
	WorkRevision          int                 `json:"work_revision"`
	PlanRevision          int                 `json:"plan_revision"`
	Binding               CandidateBinding    `json:"binding"`
	RepoRoot              string              `json:"repo_root"`
	CommonDir             string              `json:"common_dir"`
	RootIdentity          DirectoryIdentity   `json:"root_identity"`
	RepoRootIdentity      DirectoryIdentity   `json:"repo_root_identity"`
	CommonDirIdentity     DirectoryIdentity   `json:"common_dir_identity"`
	BaseOID               string              `json:"base_oid"`
	Sources               []CompositionSource `json:"sources"`
	OrderedInputOIDs      []string            `json:"ordered_input_oids"`
	CandidateOID          string              `json:"candidate_oid,omitempty"`
	TreeOID               string              `json:"tree_oid,omitempty"`
	PrivateRef            string              `json:"private_ref,omitempty"`
	PrivateRefExpectedOID string              `json:"private_ref_expected_oid,omitempty"`
	ManifestSHA256        string              `json:"manifest_sha256"`
}

type CompositionSource struct {
	TaskID                 string `json:"task_id"`
	EventID                string `json:"event_id"`
	EventSHA256            string `json:"event_sha256"`
	DecisionCommandID      string `json:"decision_command_id"`
	CandidateOID           string `json:"candidate_oid"`
	CandidateReceiptSHA256 string `json:"candidate_receipt_sha256"`
	ProviderLockSHA256     string `json:"provider_lock_sha256"`
	AdapterPayloadSHA256   string `json:"adapter_payload_sha256"`
	ArtifactSHA256         string `json:"artifact_sha256"`
}

func compositionHash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func ValidateCompositionManifest(m *CompositionManifest, c CandidateReceipt, receiptSHA256 string) error {
	if m == nil {
		return errors.New("composition_manifest_missing")
	}
	if m.Version != 1 || c.Binding == nil || m.Binding != *c.Binding || c.AttemptID != m.AttemptID || c.PlanRevision != int64(m.PlanRevision) || m.ManifestSHA256 == "" || m.AttemptID != m.Binding.AttemptID || m.SegmentID != m.Binding.SegmentID || m.WorkRevision != m.Binding.WorkRevision || m.RunID != m.Binding.RunID || m.TaskID != m.Binding.TaskID || m.PlanRevision != m.Binding.PlanRevision || m.RepoRoot != c.RepoRoot || m.CommonDir != c.CommonDir || m.BaseOID != c.BaseOID || m.CandidateOID != c.CandidateOID || m.TreeOID != c.TreeOID || m.PrivateRef != c.PrivateRef || m.PrivateRefExpectedOID != c.PrivateRefExpectedOID || m.RootIdentity != c.RootIdentity || m.RepoRootIdentity != c.RepoRootIdentity || m.CommonDirIdentity != c.CommonDirIdentity || len(m.Sources) < 2 || len(m.Sources) > 63 || len(m.Sources) != len(m.OrderedInputOIDs) || !reflect.DeepEqual(m.OrderedInputOIDs, c.OrderedInputOIDs) {
		return errors.New("composition_manifest_invalid")
	}
	receipt, err := json.Marshal(c)
	if err != nil || receiptSHA256 != compositionHash(receipt) {
		return errors.New("composition_receipt_changed")
	}
	seen := make(map[string]bool, len(m.Sources))
	for i, source := range m.Sources {
		if source.TaskID == "" || seen[source.TaskID] || source.EventID == "" || len(source.EventSHA256) != 64 || source.DecisionCommandID == "" || source.CandidateOID != m.OrderedInputOIDs[i] || len(source.CandidateReceiptSHA256) != 64 || len(source.ProviderLockSHA256) != 64 || len(source.AdapterPayloadSHA256) != 64 || source.ArtifactSHA256 != source.EventSHA256 {
			return errors.New("composition_manifest_invalid")
		}
		seen[source.TaskID] = true
	}
	copy := *m
	copy.ManifestSHA256 = ""
	raw, err := json.Marshal(copy)
	if err != nil || compositionHash(raw) != m.ManifestSHA256 {
		return errors.New("composition_manifest_invalid")
	}
	return nil
}
