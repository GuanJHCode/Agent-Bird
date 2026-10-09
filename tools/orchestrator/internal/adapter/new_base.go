package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// NewBaseReworkContext is nonsecret lineage, injected only after source authorization.
type NewBaseReworkContext struct {
	Version                 int               `json:"version"`
	SourceRunID             string            `json:"source_run_id"`
	SourceIntegrationTaskID string            `json:"source_integration_task_id"`
	IntegrationEventID      string            `json:"integration_event_id"`
	IntegrationSHA256       string            `json:"integration_sha256"`
	CandidateEventID        string            `json:"candidate_event_id"`
	CandidateSHA256         string            `json:"candidate_sha256"`
	ReviewEventID           string            `json:"review_event_id"`
	ReviewSHA256            string            `json:"review_sha256"`
	CompositionSHA256       string            `json:"composition_sha256,omitempty"`
	IntegratedHEAD          string            `json:"integrated_head"`
	TargetWorktree          string            `json:"target_worktree"`
	TargetRef               string            `json:"target_ref"`
	RunID                   string            `json:"run_id"`
	TaskMap                 map[string]string `json:"task_map"`
	Feedback                string            `json:"feedback"`
	Acceptance              string            `json:"acceptance"`
	BriefSHA256             string            `json:"brief_sha256"`
}

func (c NewBaseReworkContext) Digest() string {
	b, _ := json.Marshal(c)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
