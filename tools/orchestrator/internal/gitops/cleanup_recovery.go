package gitops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// CleanupPlan is immutable recovery authority. Names are chosen before any
// mutation; identities and inventories come only from the original receipt
// and the verified worktree administrative binding.
type CleanupPlan struct {
	Version        int               `json:"version"`
	Intent         CleanupIntent     `json:"intent"`
	RepoRoot       string            `json:"repo_root"`
	CommonDir      string            `json:"common_dir"`
	RepoIdentity   DirectoryIdentity `json:"repo_identity"`
	CommonIdentity DirectoryIdentity `json:"common_identity"`
	Root           CleanupTreePlan   `json:"root"`
	Admin          CleanupTreePlan   `json:"admin"`
}

type CleanupTreePlan struct {
	SinkIdentity   DirectoryIdentity       `json:"sink_identity"`
	Path           string                  `json:"path"`
	ParentIdentity DirectoryIdentity       `json:"parent_identity"`
	Identity       DirectoryIdentity       `json:"identity"`
	Quarantine     string                  `json:"quarantine"`
	StagePrefix    string                  `json:"stage_prefix"`
	Files          map[string]FileIdentity `json:"files"`
}

// Index authorizes one action, before the action runs. Advancing to the next
// index proves completion of the previous action. Replaying the same index is
// valid after a process stops on either side of its filesystem effect.
type CleanupStep struct {
	ReceiptSHA256 string `json:"receipt_sha256"`
	PlanSHA256    string `json:"plan_sha256"`
	Index         int    `json:"index"`
}

type CleanupRecoveryState struct {
	Plan    *CleanupPlan
	Step    int // -1 when a plan exists without any action intent
	Intent  CleanupIntent
	Outcome CleanupOutcome
	Found   bool // includes legacy cleanup records, which acquire no new authority
}

type CleanupRecoveryJournal interface {
	LoadCleanupRecovery(context.Context, string) (CleanupRecoveryState, error)
	PersistCleanupPlan(context.Context, CleanupPlan) error
	PersistCleanupStep(context.Context, CleanupStep) error
}

func CleanupPlanDigest(plan CleanupPlan) string {
	body, _ := json.Marshal(plan)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// Each tree has quarantine, move/unlink pairs, root unlink and empty sink removal. A
// private ref (when present) is deleted last. No repeated retry records accrue.
func CleanupPlanStepCount(plan CleanupPlan) int {
	n := 6 + 2*(len(plan.Root.Files)+len(plan.Admin.Files))
	if plan.Intent.PrivateRef != "" {
		n++
	}
	return n
}
