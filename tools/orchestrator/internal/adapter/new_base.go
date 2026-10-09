package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
)

// NewBaseReworkContext is nonsecret lineage, injected only after source authorization.
type NewBaseTargetIdentity struct {
	Device int64 `json:"device"`
	Inode  int64 `json:"inode"`
	Mode   int64 `json:"mode"`
	UID    int64 `json:"uid"`
}

type NewBaseReworkContext struct {
	SourceCommonDir         string                `json:"source_common_dir"`
	TargetDirectoryIdentity NewBaseTargetIdentity `json:"target_directory_identity"`
	Version                 int                   `json:"version"`
	SourceRunID             string                `json:"source_run_id"`
	SourceIntegrationTaskID string                `json:"source_integration_task_id"`
	IntegrationEventID      string                `json:"integration_event_id"`
	IntegrationSHA256       string                `json:"integration_sha256"`
	CandidateEventID        string                `json:"candidate_event_id"`
	CandidateSHA256         string                `json:"candidate_sha256"`
	ReviewEventID           string                `json:"review_event_id"`
	ReviewSHA256            string                `json:"review_sha256"`
	CompositionSHA256       string                `json:"composition_sha256,omitempty"`
	IntegratedHEAD          string                `json:"integrated_head"`
	TargetWorktree          string                `json:"target_worktree"`
	TargetRef               string                `json:"target_ref"`
	RunID                   string                `json:"run_id"`
	TaskMap                 map[string]string     `json:"task_map"`
	Feedback                string                `json:"feedback"`
	Acceptance              string                `json:"acceptance"`
	BriefSHA256             string                `json:"brief_sha256"`
}

func (c NewBaseReworkContext) Digest() string {
	b, _ := json.Marshal(c)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (c NewBaseReworkContext) Validate(run, task, base string) error {
	bad := errors.New("candidate_new_base_binding_invalid")
	if c.Version != 1 || c.RunID != run || c.SourceRunID == "" || c.SourceRunID == run || c.IntegratedHEAD != base || c.SourceIntegrationTaskID == "" || c.IntegrationEventID == "" || c.CandidateEventID == "" || c.ReviewEventID == "" || len(c.TaskMap) < 4 || len(c.TaskMap) > 64 || !filepath.IsAbs(c.TargetWorktree) || !filepath.IsAbs(c.SourceCommonDir) || c.TargetRef == "" || c.TargetDirectoryIdentity.Inode <= 0 || strings.TrimSpace(c.Feedback) == "" || strings.TrimSpace(c.Acceptance) == "" || len(c.Feedback) > 16*1024 || len(c.Acceptance) > 16*1024 || strings.ContainsRune(c.Feedback+c.Acceptance, 0) {
		return bad
	}
	for _, h := range []string{c.IntegrationSHA256, c.CandidateSHA256, c.ReviewSHA256, c.BriefSHA256} {
		b, e := hex.DecodeString(h)
		if e != nil || len(b) != 32 {
			return bad
		}
	}
	brief, _ := json.Marshal([]string{c.Feedback, c.Acceptance})
	sum := sha256.Sum256(brief)
	if hex.EncodeToString(sum[:]) != c.BriefSHA256 {
		return bad
	}
	seen := map[string]bool{}
	found := false
	for old, id := range c.TaskMap {
		if old == "" || id == "" || old == id || seen[id] {
			return bad
		}
		seen[id] = true
		found = found || id == task
	}
	if !found {
		return bad
	}
	return nil
}
