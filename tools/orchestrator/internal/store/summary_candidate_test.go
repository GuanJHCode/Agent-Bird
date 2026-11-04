package store

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/gitops"
)

func TestSummaryCandidateSurvivesReworkUnavailable(t *testing.T) {
	for _, tc := range []struct{ name, sql, reason string }{
		{"single-attempt", `UPDATE tasks SET max_attempts=1`, "attempt_limit"},
		{"provider-disabled", `INSERT INTO provider_settings VALUES('owner','claude-code',0,NULL)`, "provider_disabled"},
		{"host-offline", `UPDATE runtime_hosts SET status='offline'`, "rework_host_not_ready"},
		{"host-old-capability", `UPDATE runtime_hosts SET owner_request_changes_v2=0`, "host_upgrade_required"},
		{"budget-exhausted", `UPDATE budget_runtime SET used_ms=7200000`, "budget_exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOwnerReworkFixture(t)
			if _, err := f.db.sql.Exec(tc.sql); err != nil {
				t.Fatal(err)
			}
			assertSummaryCandidate(t, f, f.spec, tc.reason)
		})
	}
}

func assertSummaryCandidate(t *testing.T, f ownerReworkFixture, spec OwnerReworkSpec, reason string) {
	t.Helper()
	ctx := context.Background()
	summary, err := f.db.SummarizeRun(ctx, "edit", "owner", f.token)
	if err != nil {
		t.Fatal(err)
	}
	task := summary.Tasks[0]
	result := task.ReviewableResult
	if result == nil || result.CandidateOID != spec.CandidateOID || result.EventID != spec.EventID || result.EventHash != spec.EventHash || result.EventRevision != spec.EventRevision || result.ActionSlot != spec.ActionSlot || result.SegmentID != spec.SegmentID || result.WorkRevision != spec.WorkRevision {
		t.Fatalf("missing/current candidate binding: %+v want %+v", result, spec)
	}
	if task.RequestChangesBlockedReason != reason || slices.Contains(task.AllowedActions, "request-changes") || !slices.Contains(task.AllowedActions, "accept") {
		t.Fatalf("incorrect actions: %+v", task)
	}
	if _, err = f.db.QueueRequestChanges(ctx, spec); err == nil || err.Error() != reason {
		t.Fatalf("rework admitted or wrong rejection: %v", err)
	}
	if _, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "edit", WorkRevision: spec.WorkRevision, EventID: spec.EventID, EventRevision: spec.EventRevision, EventHash: spec.EventHash, ActionSlot: spec.ActionSlot, Decision: "accept", CommandID: "accept-current"}); err != nil {
		t.Fatalf("current candidate not acceptable: %v", err)
	}
}

func TestSummaryCandidateFinalSecondRevision(t *testing.T) {
	f := newOwnerReworkFixture(t)
	ctx := context.Background()
	if _, err := f.db.sql.Exec(`UPDATE tasks SET max_attempts=2`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.QueueRequestChanges(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	queued, err := f.db.SummarizeRun(ctx, "edit", "owner", f.token)
	if err != nil || queued.Tasks[0].ReviewableResult != nil {
		t.Fatalf("stale previous result projected: %+v %v", queued, err)
	}
	g, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	final := commitOwnerNextCandidate(t, f, g)
	if final.WorkRevision != 2 || final.CandidateOID == f.spec.CandidateOID {
		t.Fatalf("not a new revision: %+v", final)
	}
	assertSummaryCandidate(t, f, final, "attempt_limit")
}

func TestSummaryCandidateUnavailableStillRejectsUnverifiedEvidence(t *testing.T) {
	for _, name := range []string{"artifact-tamper", "projection-tamper", "binding-tamper", "git-ref-tamper", "legacy-v1"} {
		t.Run(name, func(t *testing.T) {
			var f ownerReworkFixture
			switch name {
			case "projection-tamper":
				f = newOwnerReworkArtifactFixture(t, func(v map[string]any) {
					v["candidate_projection_sha256"] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				})
			case "legacy-v1":
				f = newOwnerReworkArtifactFixture(t, func(v map[string]any) {
					delete(v, "candidate_projection_version")
					delete(v, "candidate_projection_sha256")
				})
			case "binding-tamper":
				f = newOwnerReworkFixture(t, func(c *gitops.CandidateReceipt) { c.Binding.TaskID = "other-task" })
			default:
				f = newOwnerReworkFixture(t)
			}
			if _, err := f.db.sql.Exec(`UPDATE tasks SET max_attempts=1`); err != nil {
				t.Fatal(err)
			}
			if name == "artifact-tamper" {
				if err := os.WriteFile(f.path, []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "git-ref-tamper" {
				var artifact struct {
					Candidate gitops.CandidateReceipt `json:"candidate"`
				}
				body, err := os.ReadFile(f.path)
				if err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(body, &artifact); err != nil {
					t.Fatal(err)
				}
				ownerFixtureGit(t, artifact.Candidate.RepoRoot, "update-ref", artifact.Candidate.PrivateRef, artifact.Candidate.BaseOID)
			}
			summary, err := f.db.SummarizeRun(context.Background(), "edit", "owner", f.token)
			if err != nil {
				t.Fatal(err)
			}
			task := summary.Tasks[0]
			if task.ReviewableResult == nil || task.ReviewableResult.CandidateOID != "" || slices.Contains(task.AllowedActions, "request-changes") || task.RequestChangesBlockedReason != "attempt_limit" {
				t.Fatalf("unverified candidate or loosened admission: %+v result=%+v", task, task.ReviewableResult)
			}
		})
	}
}

type summaryCandidateJournal struct{ count int }

func (j *summaryCandidateJournal) RecordGitProcess(ctx context.Context, record gitops.GitProcessRecord) error {
	if record.Phase == "intent" {
		j.count++
	}
	return nil
}

func TestSummaryCandidateGitVerificationRunsOnlyOnce(t *testing.T) {
	for _, mode := range []string{"eligible", "attempt-limit", "occupied-after-git"} {
		t.Run(mode, func(t *testing.T) {
			f := newOwnerReworkFixture(t)
			if mode == "attempt-limit" {
				if _, err := f.db.sql.Exec(`UPDATE tasks SET max_attempts=1`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "occupied-after-git" {
				if _, err := f.db.sql.Exec(`INSERT INTO review_decisions(action_slot,task_id,event_id,event_revision,event_hash,decision,command_id,created_at) VALUES(?,?,?,?,?,'accept','occupied','now')`, f.spec.ActionSlot, f.spec.TaskID, f.spec.EventID, f.spec.EventRevision, f.spec.EventHash); err != nil {
					t.Fatal(err)
				}
			}
			journal := &summaryCandidateJournal{}
			ctx := gitops.WithCandidateJournal(context.Background(), journal)
			summary, err := f.db.SummarizeRun(ctx, "edit", "owner", f.token)
			if err != nil {
				t.Fatal(err)
			}
			if journal.count == 0 || journal.count > 8 {
				t.Fatalf("verification exceeded single bounded Git batch: %d", journal.count)
			}
			if mode == "occupied-after-git" && summary.Tasks[0].ReviewableResult.CandidateOID != "" {
				t.Fatal("failed verification projected a partial candidate")
			}
		})
	}
}

func TestSummaryCandidateLegacyReviewBindingRemainsAcceptable(t *testing.T) {
	f := newOwnerReworkArtifactFixture(t, func(v map[string]any) {
		delete(v, "candidate_projection_version")
		delete(v, "candidate_projection_sha256")
	})
	ctx := context.Background()
	summary, err := f.db.SummarizeRun(ctx, "edit", "owner", f.token)
	if err != nil {
		t.Fatal(err)
	}
	task := summary.Tasks[0]
	if task.ReviewableResult == nil || task.ReviewableResult.CandidateOID != "" || task.RequestChangesBlockedReason != "rework_artifact_upgrade_required" || !slices.Contains(task.AllowedActions, "accept") {
		t.Fatalf("legacy review binding changed: %+v", task)
	}
	if _, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "edit", WorkRevision: f.spec.WorkRevision, EventID: f.spec.EventID, EventRevision: f.spec.EventRevision, EventHash: f.spec.EventHash, ActionSlot: f.spec.ActionSlot, Decision: "accept", CommandID: "accept-legacy"}); err != nil {
		t.Fatalf("legacy review rejected: %v", err)
	}
}

func TestSummaryCandidateAfterHostRetirement(t *testing.T) {
	f := newOwnerReworkFixture(t)
	if _, err := f.db.sql.Exec(`UPDATE tasks SET max_attempts=1`); err != nil {
		t.Fatal(err)
	}
	if err := f.db.RetireHost(context.Background(), f.host); err != nil {
		t.Fatal(err)
	}
	assertSummaryCandidate(t, f, f.spec, "attempt_limit")
}
