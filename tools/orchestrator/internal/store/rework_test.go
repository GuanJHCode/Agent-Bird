package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type reworkFixture struct {
	db     *DB
	spec   ReworkSpec
	host   string
	edit   contract.LaunchCommand
	result contract.Event
}

// Use actual admission, scheduling, durable Host events and owner acceptance.
// Only the Provider/Git receipt is a fixture in this store-level test.
func newReworkFixture(t *testing.T) reworkFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target := map[string]any{"worktree": "/private/repo", "ref": "refs/heads/main", "base_oid": strings.Repeat("a", 40)}
	payload := func(stage, source string) json.RawMessage {
		p := map[string]any{"directory": "/private/" + stage, "prompt": "original brief"}
		if stage == "edit" {
			p["provider"] = "claude-code"
			p["profile"] = map[string]any{"version": 1, "role": "implementer", "permission": "workspace-write"}
			p["candidate_workspace"] = map[string]any{"version": 1, "repo_root": "/private/repo", "base_oid": strings.Repeat("a", 40), "paths": []string{"add.py"}}
		} else {
			p["candidate_action"] = map[string]any{"version": 1, "operation": stage, "source_task": source, "target": target}
			if stage == "validate" {
				p["candidate_action"].(map[string]any)["command"] = []string{"/usr/bin/true"}
			}
			if stage == "review" {
				p["provider"] = "claude-code"
				p["profile"] = map[string]any{"version": 1, "role": "reviewer", "permission": "read-only"}
			} else {
				p["kind"] = "candidate"
			}
			if stage == "integrate" {
				p["directory"] = "/private/repo"
			}
		}
		b, _ := json.Marshal(p)
		return b
	}
	stages := []string{"edit", "validate", "review", "integrate"}
	tasks := []TaskSpec{}
	for i, s := range stages {
		source := ""
		deps := []string{}
		if i > 0 {
			source = stages[i-1]
			deps = []string{source}
		}
		tasks = append(tasks, TaskSpec{ID: s, RunID: "run", MaxAttempts: 3, Dependencies: deps, AdapterPayload: payload(s, source)})
	}
	plan, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 7, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"}, Tasks: tasks})
	if err != nil {
		t.Fatal(err)
	}
	h, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "gen", PID: 10, Birth: "host", Executable: "/private/bin/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	f := reworkFixture{db: db, host: h}
	var candidate any
	var prev contract.Event
	var seq int64
	for _, stage := range stages[:3] {
		grant, err := db.ClaimReady(ctx, h, 1, 2)
		if err != nil {
			t.Fatal(err)
		}
		if grant.TaskID != stage {
			t.Fatalf("task=%s want=%s", grant.TaskID, stage)
		}
		binding := map[string]any{"run_id": "run", "task_id": stage, "attempt_id": grant.AttemptID, "segment_id": grant.SegmentID, "work_revision": 1, "plan_revision": 7}
		if stage == "edit" {
			f.edit = grant
			candidate = map[string]any{"binding": binding, "repo_root": "/private/repo", "base_oid": strings.Repeat("a", 40), "candidate_oid": strings.Repeat("b", 40), "tree_oid": strings.Repeat("c", 40)}
		}
		artifact := map[string]any{"version": 1, "candidate": candidate}
		kind := contract.EventResult
		if stage != "edit" {
			artifact["stage"] = stage
			artifact["binding"] = binding
			artifact["input_event_id"] = prev.EventID
			artifact["input_sha256"] = prev.PayloadHash
			artifact["validation_digest"] = strings.Repeat("d", 64)
			artifact["validation"] = map[string]any{"passed": true}
		}
		if stage == "review" {
			artifact["review"] = map[string]any{"decision": "reject", "summary": "negative operands were not covered"}
			kind = contract.EventFailed
		}
		b, _ := json.Marshal(artifact)
		path := filepath.Join(dir, stage+".json")
		if err = os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		seq++
		e := contract.Event{Version: 1, ProducerID: h, EventID: stage + "-result", RunID: "run", TaskID: stage, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: grant.CommandID, Sequence: seq, Kind: kind, PayloadHash: runtimeHash(string(b)), Artifact: &contract.ArtifactRef{ID: stage, Path: path, Size: int64(len(b)), SHA256: runtimeHash(string(b))}}
		if _, err = db.CommitHostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
		seq++
		done := e
		done.EventID = stage + "-exited"
		done.Sequence = seq
		done.Kind = contract.EventExited
		zero := 0
		done.ExitCode = &zero
		if _, err = db.CommitHostEvent(ctx, done); err != nil {
			t.Fatal(err)
		}
		page, err := db.CollectPending(ctx, stage, "", 0, false)
		if err != nil || len(page.Events) != 1 {
			t.Fatalf("collect %+v %v", page, err)
		}
		prev = page.Events[0]
		if stage != "review" {
			if _, err = db.ReviewResult(ctx, ReviewSpec{TaskID: stage, WorkRevision: 1, EventID: prev.EventID, EventRevision: prev.EventRevision, EventHash: prev.PayloadHash, ActionSlot: prev.ActionSlot, Decision: "accept", CommandID: "accept-" + stage}); err != nil {
				t.Fatal(err)
			}
		}
		if stage == "edit" {
			f.result = e
			f.spec.Candidate = ReworkEvent{prev.EventID, prev.EventRevision, prev.PayloadHash}
		}
	}
	f.spec.TaskID = "edit"
	f.spec.WorkRevision = 1
	f.spec.CandidateOID = strings.Repeat("b", 40)
	f.spec.ReviewTaskID = "review"
	f.spec.ReviewWorkRevision = 1
	f.spec.Review = ReworkEvent{prev.EventID, prev.EventRevision, prev.PayloadHash}
	f.spec.ActionSlot = prev.ActionSlot
	f.spec.Feedback = "Cover the negative operand case"
	f.spec.Acceptance = "add(-2,3) must equal 1"
	f.spec.CommandID = "rework-1"
	return f
}

func TestReworkRevisesWholeCandidateChainAndReplaysWithoutRepeating(t *testing.T) {
	f := newReworkFixture(t)
	ctx := context.Background()
	receipt, err := f.db.QueueRework(ctx, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "rework_queued" || receipt.WorkRevision != 2 || len(receipt.TaskRevisions) != 4 || receipt.RevisionSHA256 == "" {
		t.Fatalf("receipt=%+v", receipt)
	}
	for _, id := range []string{"edit", "validate", "review", "integrate"} {
		var revision int
		if err = f.db.sql.QueryRow(`SELECT work_revision FROM task_runtime WHERE task_id=?`, id).Scan(&revision); err != nil || revision != 2 {
			t.Fatalf("revision %s=%d %v", id, revision, err)
		}
	}
	next, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if next.TaskID != "edit" || next.WorkRevision != 2 || next.PlanRevision != 7 {
		t.Fatalf("grant=%+v", next)
	}
	var payload struct {
		Directory, Prompt string
		Workspace         struct {
			BaseOID string          `json:"base_oid"`
			Rework  json.RawMessage `json:"rework"`
		} `json:"candidate_workspace"`
	}
	if err = json.Unmarshal(next.AdapterPayload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Directory == "/private/edit" || payload.Workspace.BaseOID != f.spec.CandidateOID || len(payload.Workspace.Rework) == 0 || !strings.Contains(payload.Prompt, f.spec.Acceptance) || !strings.Contains(payload.Prompt, "negative operands were not covered") {
		t.Fatalf("rework did not carry candidate and actionable feedback: %s", next.AdapterPayload)
	}
	f.spec.CommandID = "same-decision-new-command"
	replay, err := f.db.QueueRework(ctx, f.spec)
	if err != nil || replay.RevisionSHA256 != receipt.RevisionSHA256 {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	f.spec.Acceptance = "conflicting criteria"
	if _, err = f.db.QueueRework(ctx, f.spec); err == nil {
		t.Fatal("conflicting decision accepted")
	}
	var attempts int
	f.db.sql.QueryRow(`SELECT COUNT(*) FROM attempts WHERE task_id='edit'`).Scan(&attempts)
	if attempts != 2 {
		t.Fatalf("attempt budget reset or duplicate execution: %d", attempts)
	}
	// Old accepted-result replay remains idempotent, but cannot complete revision 2.
	_, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "edit", WorkRevision: 1, EventID: f.result.EventID, EventRevision: f.spec.Candidate.EventRevision, EventHash: f.result.PayloadHash, ActionSlot: "action-" + runtimeHash(f.host + "\x00" + f.result.EventID)[:32], Decision: "accept", CommandID: "accept-edit"})
	if err != nil {
		t.Fatal(err)
	}
	var status string
	f.db.sql.QueryRow(`SELECT status FROM tasks WHERE id='edit'`).Scan(&status)
	if status != "running" {
		t.Fatalf("old acceptance changed current revision: %s", status)
	}
}

func TestReworkRejectsUnsafeOrStaleChangesAtomically(t *testing.T) {
	for _, name := range []string{"stale_candidate", "stale_feedback", "tampered_artifact", "budget", "active", "integration_started", "quota", "old_revision"} {
		t.Run(name, func(t *testing.T) {
			f := newReworkFixture(t)
			switch name {
			case "stale_candidate":
				f.spec.Candidate.EventHash = strings.Repeat("0", 64)
			case "stale_feedback":
				f.spec.Review.EventRevision++
			case "old_revision":
				f.spec.WorkRevision++
			case "tampered_artifact":
				os.WriteFile(f.result.Artifact.Path, []byte("tampered"), 0600)
			case "budget":
				f.db.sql.Exec(`UPDATE tasks SET max_attempts=1 WHERE id='validate'`)
			case "active":
				f.db.sql.Exec(`UPDATE segment_runtime SET status='unknown' WHERE segment_id=?`, f.edit.SegmentID)
			case "integration_started":
				f.db.sql.Exec(`INSERT INTO attempts(id,task_id,attempt_no,status,created_at) VALUES('integration-attempt','integrate',1,'failed','')`)
			case "quota":
				f.db.runControlLimit = 1
			}
			if _, err := f.db.QueueRework(context.Background(), f.spec); err == nil {
				t.Fatal("unsafe rework accepted")
			}
			var rev, count int
			f.db.sql.QueryRow(`SELECT work_revision FROM task_runtime WHERE task_id='edit'`).Scan(&rev)
			f.db.sql.QueryRow(`SELECT COUNT(*) FROM retry_decisions`).Scan(&count)
			if rev != 1 || count != 0 {
				t.Fatalf("partial transaction: rev=%d decisions=%d", rev, count)
			}
		})
	}
}

func TestReworkLaunchReplayBindsRevisionPayload(t *testing.T) {
	f := newReworkFixture(t)
	ctx := context.Background()
	if _, err := f.db.QueueRework(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	grant, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := f.db.PendingLaunches(ctx, f.host, 1)
	if err != nil || len(pending) != 1 || pending[0].LaunchIntentHash != grant.LaunchIntentHash {
		t.Fatalf("replay %+v %v", pending, err)
	}
	if _, err = f.db.sql.Exec(`UPDATE task_runtime SET adapter_payload=json_set(adapter_payload,'$.prompt','unbound replacement') WHERE task_id='edit'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.PendingLaunches(ctx, f.host, 1); err == nil {
		t.Fatal("replay accepted changed rework prompt")
	}
}

func TestReworkDecisionCannotReplayAsOrdinaryRetry(t *testing.T) {
	f := newReworkFixture(t)
	ctx := context.Background()
	if _, err := f.db.QueueRework(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	var segment string
	f.db.sql.QueryRow(`SELECT source_segment_id FROM retry_decisions WHERE action_slot=?`, f.spec.ActionSlot).Scan(&segment)
	_, err := f.db.QueueRetry(ctx, RetrySpec{TaskID: "edit", WorkRevision: 1, EventID: f.spec.Review.EventID, EventRevision: f.spec.Review.EventRevision, EventHash: f.spec.Review.EventHash, ActionSlot: f.spec.ActionSlot, SegmentID: segment, NextAttemptNo: 2, CommandID: "ordinary-retry"})
	if err == nil {
		t.Fatal("rework receipt was accepted as ordinary retry")
	}
}

func TestReworkFencesOldHostEventsButKeepsDurableReplay(t *testing.T) {
	f := newReworkFixture(t)
	ctx := context.Background()
	if _, err := f.db.QueueRework(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.CommitHostEvent(ctx, f.result); err != nil {
		t.Fatalf("durable old replay lost ACK: %v", err)
	}
	late := f.result
	late.EventID = "late-old-result"
	late.Sequence = 7
	late.WorkRevision = 2
	if _, err := f.db.CommitHostEvent(ctx, late); err == nil {
		t.Fatal("old segment injected event into new task revision")
	}
	var status string
	f.db.sql.QueryRow(`SELECT status FROM report_capabilities WHERE segment_id=?`, f.edit.SegmentID).Scan(&status)
	if status != "revoked" {
		t.Fatalf("old Worker report capability remains %s", status)
	}
}

func TestReworkRejectsUnassociatedOldQuestion(t *testing.T) {
	f := newReworkFixture(t)
	_, err := f.db.sql.Exec(`INSERT INTO report_events(event_id,capability_id,sequence,kind,payload_hash,body_json,event_revision,action_slot,delivery_status,accounted_bytes,created_at) VALUES('old-question',?,1,'question','hash','{}',1,'old-question-slot','pending_session',2,'')`, f.edit.ReportCapabilityID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.QueueRework(context.Background(), f.spec); err == nil {
		t.Fatal("pending-session old question would block new revision questions")
	}
}

func TestReworkRevokesNewReportsButRetainsExactDurableACK(t *testing.T) {
	f := newReworkFixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	g := f.edit
	if _, err := f.db.RegisterReportCapability(ctx, contract.ReportCapabilityRegistration{CapabilityID: g.ReportCapabilityID, ProducerID: f.host, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, ExecutionEpoch: g.ExecutionEpoch, TokenHash: runtimeHash("token"), CapabilityDir: dir}); err != nil {
		t.Fatal(err)
	}
	report := ReportEventSpec{CapabilityID: g.ReportCapabilityID, Token: "token", EventID: "old-progress", Sequence: 1, Kind: "progress", Payload: json.RawMessage(`{"status":"resource_sample","sequence":1,"elapsed_ms":10}`)}
	first, err := f.db.CommitReportEvent(ctx, report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.QueueRework(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	again, err := f.db.CommitReportEvent(ctx, report)
	if err != nil || again != first {
		t.Fatalf("durable old report ACK lost: %+v %v", again, err)
	}
	report.EventID = "new-old-report"
	report.Sequence++
	if _, err = f.db.CommitReportEvent(ctx, report); err == nil {
		t.Fatal("revoked cap wrote new event")
	}
}

func TestSubmitCannotForgeReworkLineage(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "adapter", true: "fallback"}[fallback], func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			forged := json.RawMessage(`{"candidate_workspace":{"version":1,"rework":{"version":1,"previous_work_revision":0}}}`)
			task := TaskSpec{ID: "task", RunID: "new", MaxAttempts: 3, AdapterPayload: forged}
			if fallback {
				task.AdapterPayload = json.RawMessage(`{}`)
				task.FallbackPayloads = []json.RawMessage{forged}
			}
			_, err = db.SubmitPlan(context.Background(), PlanSpec{Run: RunSpec{ID: "new", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"}, Tasks: []TaskSpec{task}})
			if err == nil {
				t.Fatal("submit accepted forged rework lineage")
			}
		})
	}
}

func TestReworkCannotQueueOversizedWorkerGrant(t *testing.T) {
	f := newReworkFixture(t)
	f.spec.Feedback = strings.Repeat("x", 16*1024)
	f.spec.Acceptance = strings.Repeat("y", 16*1024)
	if _, err := f.db.QueueRework(context.Background(), f.spec); err == nil {
		t.Fatal("queued adapter larger than durable IPC handoff permits")
	}
	var rev int
	f.db.sql.QueryRow(`SELECT work_revision FROM task_runtime WHERE task_id='edit'`).Scan(&rev)
	if rev != 1 {
		t.Fatalf("oversized revision committed: %d", rev)
	}
}

func TestReviewCannotInjectOversizedDecisionIntoNextGrant(t *testing.T) {
	f := newReworkFixture(t)
	_, err := f.db.ReviewResult(context.Background(), ReviewSpec{TaskID: "edit", WorkRevision: 1, EventID: f.result.EventID, EventRevision: f.spec.Candidate.EventRevision, EventHash: f.result.PayloadHash, ActionSlot: "action-" + runtimeHash(f.host + "\x00" + f.result.EventID)[:32], Decision: "accept", CommandID: strings.Repeat("x", 50*1024)})
	if err == nil {
		t.Fatal("oversized owner command accepted")
	}
}

func TestOversizedLaunchRollsBackReservation(t *testing.T) {
	f := newReworkFixture(t)
	ctx := context.Background()
	if _, err := f.db.QueueRework(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"prompt": strings.Repeat("x", 64*1024)})
	if _, err := f.db.sql.Exec(`UPDATE task_runtime SET adapter_payload=? WHERE task_id='edit'`, string(payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ClaimReady(ctx, f.host, 1, 2); err == nil {
		t.Fatal("oversized grant reserved resources before failed IPC write")
	}
	var attempts int
	f.db.sql.QueryRow(`SELECT COUNT(*) FROM attempts WHERE task_id='edit'`).Scan(&attempts)
	if attempts != 1 {
		t.Fatalf("oversized launch leaked attempt %d", attempts)
	}
}

func TestReworkLateOldHostEventIsHistoricalAndACKable(t *testing.T) {
	f := newReworkFixture(t)
	ctx := context.Background()
	if _, err := f.db.QueueRework(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	late := f.result
	late.EventID = "late-original-revision"
	late.Sequence = 7
	late.Kind = contract.EventUnknown
	ack, err := f.db.CommitHostEvent(ctx, late)
	if err != nil || ack.Status != "durable" {
		t.Fatalf("historical event lost durable ACK: %+v %v", ack, err)
	}
	var taskStatus, delivery string
	f.db.sql.QueryRow(`SELECT status FROM tasks WHERE id='edit'`).Scan(&taskStatus)
	f.db.sql.QueryRow(`SELECT delivery_status FROM runtime_events WHERE event_id=?`, late.EventID).Scan(&delivery)
	if taskStatus != "ready" || delivery != "internal" {
		t.Fatalf("old event changed current state or became actionable: %s %s", taskStatus, delivery)
	}
}
