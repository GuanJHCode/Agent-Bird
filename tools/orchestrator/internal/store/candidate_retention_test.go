package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitopsworker"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func candidateRetentionFixture(t *testing.T) (*hostRetentionFixture, gitops.CandidateResourceHeader) {
	t.Helper()
	f := newHostRetentionFixture(t, false, false)
	g := f.grant
	payload := `{"provider":"claude-code","candidate_workspace":{"version":1,"auto_directory":true,"repo_root":"/repo","base_oid":"` + strings.Repeat("a", 40) + `","paths":["file"]}}`
	if _, e := f.db.sql.Exec(`UPDATE task_runtime SET adapter_payload=? WHERE task_id=?`, payload, g.TaskID); e != nil {
		t.Fatal(e)
	}
	source := gitops.CandidateResourceSource{Kind: "producer", HostID: g.HostID, CommandID: g.CommandID, Binding: gitops.CandidateBinding{RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, PlanRevision: 1}, JournalRelative: ".gitops-journal/" + runtimeHash(g.RunID+"\x00"+g.TaskID+"\x00"+g.AttemptID+"\x00"+g.SegmentID), ProjectionSHA256: runtimeHash("public receipt")}
	id := gitops.DirectoryIdentity{Device: 1, Inode: 2, Mode: int64(os.ModeDir | 0700), UID: int64(os.Geteuid())}
	h := gitops.CandidateResourceHeader{Version: 1, Source: source, ReceiptSHA256: source.ProjectionSHA256, JournalSHA256: runtimeHash("journal"), JournalIdentity: gitops.FileIdentity{Device: 1, Inode: 3, Size: 20, Mode: 0600}, SpoolIdentity: id, NamespaceIdentity: id, DirectoryIdentity: id}
	h.ResourceID = gitops.CandidateResourceID(source, h.ReceiptSHA256)
	h.SealSHA256 = gitops.CandidateResourceSealDigest(h)
	raw, _ := json.Marshal(h)
	event := diagnosticOutcomeEvent(g, contract.EventCandidateResourceRegistered, 4)
	event.CandidateResource = raw
	event.PayloadHash = runtimeHash(string(raw))
	if _, e := f.db.CommitHostEvent(context.Background(), event); e != nil {
		t.Fatal("registration", e)
	}
	if _, e := f.db.CommitHostEvent(context.Background(), event); e != nil {
		t.Fatal("registration replay", e)
	}
	var count int
	if e := f.db.sql.QueryRow(`SELECT count(*) FROM candidate_resources`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("resources=%d err=%v", count, e)
	}
	return f, h
}
func candidatePlanAt(t *testing.T, f *hostRetentionFixture, now time.Time, request string) CandidateRetentionPlan {
	t.Helper()
	p, e := f.db.planCandidateRetentionAt(context.Background(), "controller", f.token, CandidateRetentionPlanRequest{Version: 1, RunID: f.grant.RunID, RequestID: request, Limit: 8}, now)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestCandidateRetentionAgeResolutionAndConsumption(t *testing.T) {
	for _, name := range []string{"success_boundary", "success_expired", "failed_unresolved", "failed_resolved", "pending_delivery"} {
		t.Run(name, func(t *testing.T) {
			f, _ := candidateRetentionFixture(t)
			now := f.at.Add(31 * 24 * time.Hour)
			if name == "success_boundary" {
				now = f.at.Add(30 * 24 * time.Hour)
			}
			if strings.HasPrefix(name, "failed_") {
				if _, e := f.db.sql.Exec(`UPDATE tasks SET status='failed' WHERE id=?`, f.grant.TaskID); e != nil {
					t.Fatal(e)
				}
			}
			if name == "failed_resolved" {
				r := CandidateRetentionResolutionRequest{Version: 1, RunID: f.grant.RunID, RequestID: "resolve", Resolution: "resolved", Reason: "owner resolved failure"}
				first, e := f.db.resolveCandidateRetentionAt(context.Background(), "controller", f.token, r, f.at)
				if e != nil {
					t.Fatal(e)
				}
				again, e := f.db.resolveCandidateRetentionAt(context.Background(), "controller", f.token, r, now)
				if e != nil || first != again {
					t.Fatal("resolution replay", again, e)
				}
				now = f.at.Add(7 * 24 * time.Hour)
				p := candidatePlanAt(t, f, now, "boundary")
				if len(p.Items) != 0 {
					t.Fatal("failure collected at boundary")
				}
				now = now.Add(time.Second)
			}
			if name == "pending_delivery" {
				if _, e := f.db.sql.Exec(`UPDATE runtime_events SET delivery_status='pending' WHERE event_id=?`, f.event.EventID); e != nil {
					t.Fatal(e)
				}
			}
			p := candidatePlanAt(t, f, now, "age")
			want := name == "success_expired" || name == "failed_resolved"
			if (len(p.Items) == 1) != want {
				t.Fatalf("plan=%+v", p)
			}
		})
	}
}
func TestCandidateMaintenanceGenerationFenceAndJournalRecovery(t *testing.T) {
	ctx := context.Background()
	f, h := candidateRetentionFixture(t)
	now := f.at.Add(31 * 24 * time.Hour)
	p := candidatePlanAt(t, f, now, "first")
	s, e := f.db.prepareCandidateMaintenanceAt(ctx, "controller", f.token, p, h.ResourceID, "/private/tmp/orchestrator", runtimeHash("executable"), now)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.db.sql.Exec(`UPDATE tasks SET status='pending' WHERE id=?`, f.grant.TaskID); e == nil {
		t.Fatal("retired run revived")
	}
	if _, e = f.db.sql.Exec(`UPDATE task_runtime SET work_revision=2 WHERE task_id=?`, f.grant.TaskID); e == nil {
		t.Fatal("retired work revision changed")
	}
	if _, e = f.db.sql.Exec(`DELETE FROM candidate_run_retirements WHERE run_id=?`, f.grant.RunID); e == nil {
		t.Fatal("retirement index deleted")
	}
	j, e := gitopsworker.OpenExistingMaintenanceJournal(s.Grant.OperationJournal, s.Grant.JournalIdentity)
	if e != nil {
		t.Fatal(e)
	}
	j.Close()
	same, e := f.db.prepareCandidateMaintenanceAt(ctx, "controller", f.token, p, h.ResourceID, "/private/tmp/orchestrator", runtimeHash("executable"), now)
	if e != nil || same.Grant.GrantSHA256 != s.Grant.GrantSHA256 {
		t.Fatal("prepare replay", e)
	}
	// The current process is intentionally alive: recovery must not trust state
	// alone as an original Wait or terminate an unrelated process.
	birth, e := process.BirthContext(ctx, os.Getpid())
	if e != nil {
		t.Fatal(e)
	}
	child := process.Identity{PID: os.Getpid(), PGID: os.Getpid(), Birth: birth, BirthKnown: true, Executable: s.Grant.Executable, ExecutableSHA256: s.Grant.ExecutableSHA256, StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if _, e = f.db.MarkCandidateMaintenanceRunning(ctx, "controller", f.token, s.Grant, child); e != nil {
		t.Fatal(e)
	}
	if _, e = f.db.RecoverCandidateMaintenance(ctx, "controller", f.token, s.Grant); e == nil {
		t.Fatal("live actor recovered")
	}
	out := CandidateMaintenanceOutcome{Version: 1, State: "pending", Code: "interrupted"}
	if _, e = f.db.FinishCandidateMaintenance(ctx, s.Grant, child, out); e != nil {
		t.Fatal(e)
	}
	p = candidatePlanAt(t, f, now, "second")
	next, e := f.db.prepareCandidateMaintenanceAt(ctx, "controller", f.token, p, h.ResourceID, s.Grant.Executable, s.Grant.ExecutableSHA256, now)
	if e != nil {
		t.Fatal(e)
	}
	if next.Grant.Generation != 2 || next.Grant.OperationJournal != s.Grant.OperationJournal || next.Grant.JournalIdentity != s.Grant.JournalIdentity {
		t.Fatal("recovery adopted different journal")
	}
	if _, e = f.db.FinishCandidateMaintenance(ctx, s.Grant, child, CandidateMaintenanceOutcome{Version: 1, State: "removed", Cleanup: &gitops.CleanupOutcome{State: gitops.CleanupRemoved}}); e == nil {
		t.Fatal("old generation finalized new generation")
	}
	if _, e = f.db.RecoverCandidateMaintenance(ctx, "controller", f.token, next.Grant); e != nil {
		t.Fatal("unstarted recovery", e)
	}
	path := next.Grant.OperationJournal
	if e = os.Rename(path, path+"-old"); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(path, 0700); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"events.jsonl", "events.lock"} {
		if e = os.WriteFile(filepath.Join(path, name), nil, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = gitopsworker.OpenExistingMaintenanceJournal(path, next.Grant.JournalIdentity); e == nil {
		t.Fatal("replacement operation journal adopted")
	}
}
func TestCandidateGCConsentAndDisableDuringRound(t *testing.T) {
	ctx := context.Background()
	f, _ := candidateRetentionFixture(t)
	calls := 0
	execute := func(ctx context.Context, controller, token string, p CandidateRetentionPlan) error {
		calls++
		_, e := f.db.ControlCandidateGC(ctx, "controller", f.token, "disable", CandidateGCRequest{Version: 1, RunID: f.grant.RunID, ExpectedRevision: 1})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.db.prepareCandidateMaintenanceAt(ctx, controller, token, p, p.Items[0].ResourceID, "/private/tmp/orchestrator", runtimeHash("executable"), f.at.Add(31*24*time.Hour)); e != CodeError("candidate_gc_disabled") {
			t.Fatalf("disabled preparation=%v", e)
		}
		return e
	}
	if e := f.db.RunCandidateGC(ctx, f.at.Add(31*24*time.Hour), execute); e != nil || calls != 0 {
		t.Fatal("default collection", e, calls)
	}
	r := CandidateGCRequest{Version: 1, RunID: f.grant.RunID}
	if _, e := f.db.ControlCandidateGC(ctx, "controller", f.token, "enable", r); e != nil {
		t.Fatal(e)
	}
	if e := f.db.RunCandidateGC(ctx, f.at.Add(31*24*time.Hour), execute); e != nil || calls != 1 {
		t.Fatal("enabled collection", e, calls)
	}
	status, e := f.db.ControlCandidateGC(ctx, "controller", f.token, "status", r)
	if e != nil || status.Enabled || status.Revision != 2 {
		t.Fatal(status, e)
	}
}
