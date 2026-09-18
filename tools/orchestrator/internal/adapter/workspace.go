package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// CandidateWorkspace requests a fresh Host-owned linked worktree. Paths are
// exact repository-relative files, never globs or permission expansion rules.
type CandidateWorkspace struct {
	AutoDirectory bool           `json:"auto_directory,omitempty"`
	Version       int            `json:"version"`
	RepoRoot      string         `json:"repo_root"`
	BaseOID       string         `json:"base_oid"`
	Paths         []string       `json:"paths"`
	Rework        *ReworkContext `json:"rework,omitempty"`
}

// ReworkContext records the owner's revised brief and immutable evidence. It
// grants no permissions and is bound into the new candidate's Host receipt.
type ReworkContext struct {
	Version              int    `json:"version"`
	RunID                string `json:"run_id"`
	TaskID               string `json:"task_id"`
	PreviousWorkRevision int    `json:"previous_work_revision"`
	PreviousCandidateOID string `json:"previous_candidate_oid"`
	CandidateEventID     string `json:"candidate_event_id"`
	CandidateSHA256      string `json:"candidate_sha256"`
	ReviewEventID        string `json:"review_event_id"`
	ReviewSHA256         string `json:"review_sha256"`
	ReviewSummary        string `json:"review_summary"`
	Feedback             string `json:"feedback"`
	Acceptance           string `json:"acceptance"`
}

func (r ReworkContext) Digest() string {
	body, _ := json.Marshal(r)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func cloneWorkspace(value *CandidateWorkspace) *CandidateWorkspace {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Paths = append([]string(nil), value.Paths...)
	if value.Rework != nil {
		r := *value.Rework
		copy.Rework = &r
	}
	return &copy
}

func (i Invocation) CandidateWorkspace() *CandidateWorkspace { return cloneWorkspace(i.workspace) }

func validateWorkspace(req Request) error {
	if req.Workspace == nil {
		if req.Profile != nil && req.Profile.Permission == WorkspaceWrite {
			return errors.New("managed_workspace_required")
		}
		return nil
	}
	if req.Workspace.Version != 1 || req.Profile == nil || req.Profile.Role != Implementer || req.Profile.Permission != WorkspaceWrite || req.Session.ID != "" {
		return errors.New("candidate_workspace_profile_invalid")
	}
	return nil
}
