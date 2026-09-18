package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type reviewRetryFixture struct {
	control string
	db      *DB
	host    string
	grant   contract.LaunchCommand
	event   contract.Event
	spec    RetrySpec
}

func newReviewRetryFixture(t *testing.T, terminal string, sharedGroup ...bool) reviewRetryFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	lock := &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: adapter.BinaryPin{Path: "/private/bin/claude", Version: "claude-test", SHA256: strings.Repeat("a", 64)}}
	target := adapter.CandidateTarget{Worktree: "/private/repo", Ref: "refs/heads/main", BaseOID: strings.Repeat("a", 40)}
	ids := []string{"edit", "validate", "review", "integrate"}
	var tasks []TaskSpec
	for i, id := range ids {
		p := adapter.InvocationPayload{Directory: filepath.Join(dir, id)}
		task := TaskSpec{ID: id, RunID: "run", MaxAttempts: 3}
		if i == 0 {
			p.Provider = "claude-code"
			p.CandidateWorkspace = &adapter.CandidateWorkspace{Version: 1}
		} else {
			task.Dependencies = []string{ids[i-1]}
			p.CandidateAction = &adapter.CandidateAction{Version: 1, Operation: id, SourceTask: ids[i-1], Target: target}
			if id == "review" {
				p.Provider = "claude-code"
				p.ProviderLock = lock
				p.Profile = &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 180000}
				p.Prompt = "independently review"
			} else {
				p.Kind = "candidate"
			}
			if id == "validate" {
				p.CandidateAction.Command = []string{"/usr/bin/true"}
			}
		}
		task.AdapterPayload, _ = json.Marshal(p)
		tasks = append(tasks, task)
	}
	if len(sharedGroup) > 0 && sharedGroup[0] {
		tasks = append(tasks, TaskSpec{ID: "side-task", RunID: "run", MaxAttempts: 3, BudgetGroupID: "review", Dependencies: []string{"validate"}})
	}
	receipt, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 7, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"}, Tasks: tasks})
	if err != nil {
		t.Fatal(err)
	}
	h, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "gen", PID: 10, Birth: "host", Executable: "/private/bin/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	f := reviewRetryFixture{db: db, host: h, control: receipt.ControlToken}
	var seq int64
	for _, id := range ids[:3] {
		g, err := db.ClaimReady(ctx, h, 1, 2)
		if err != nil {
			t.Fatal(err)
		}
		if g.TaskID != id {
			t.Fatalf("task=%s want=%s", g.TaskID, id)
		}
		seq++
		e := contract.Event{Version: 1, ProducerID: h, EventID: id + "-result", RunID: g.RunID, TaskID: id, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, ExecutionEpoch: 1, CommandID: g.CommandID, Sequence: seq, Kind: contract.EventResult, PayloadHash: runtimeHash("stopped")}
		if id == "review" {
			e.Kind = terminal
			f.grant = g
		} else {
			body, _ := json.Marshal(map[string]any{"stage": id, "validation": map[string]any{"passed": true}, "candidate": map[string]any{"candidate_oid": strings.Repeat("b", 40)}})
			path := filepath.Join(dir, id+".json")
			if err = os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			e.PayloadHash = runtimeHash(string(body))
			e.Artifact = &contract.ArtifactRef{Path: path, Size: int64(len(body)), SHA256: e.PayloadHash}
		}
		if _, err = db.CommitHostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
		seq++
		done := e
		done.EventID = id + "-exited"
		done.Sequence = seq
		done.Kind = contract.EventExited
		zero := 0
		done.ExitCode = &zero
		done.ActiveMS = 1234
		if _, err = db.CommitHostEvent(ctx, done); err != nil {
			t.Fatal(err)
		}
		page, err := db.CollectPending(ctx, id, "", 0, false)
		if err != nil || len(page.Events) != 1 {
			t.Fatalf("page=%+v err=%v", page, err)
		}
		e = page.Events[0]
		if id != "review" {
			if _, err = db.ReviewResult(ctx, ReviewSpec{TaskID: id, WorkRevision: 1, EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, Decision: "accept", CommandID: "accept-" + id}); err != nil {
				t.Fatal(err)
			}
		} else {
			f.event = e
			f.spec = RetrySpec{TaskID: id, WorkRevision: 1, EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, SegmentID: e.SegmentID, NextAttemptNo: 2, CommandID: "retry-review"}
		}
	}
	return f
}

func TestCandidateReviewRetryUsesFreshBoundAttemptWithoutResettingBudget(t *testing.T) {
	for _, terminal := range []string{contract.EventStopped, contract.EventFailed} {
		t.Run(terminal, func(t *testing.T) {
			f := newReviewRetryFixture(t, terminal)
			ctx := context.Background()
			first, err := f.db.QueueRetry(ctx, f.spec)
			if err != nil {
				t.Fatal(err)
			}
			f.spec.CommandID = "duplicate"
			second, err := f.db.QueueRetry(ctx, f.spec)
			if err != nil || first != second {
				t.Fatalf("duplicate=%+v %v", second, err)
			}
			g, err := f.db.ClaimReady(ctx, f.host, 1, 2)
			if err != nil {
				t.Fatal(err)
			}
			old, _ := adapter.DecodeInvocationPayload(f.grant.AdapterPayload)
			next, _ := adapter.DecodeInvocationPayload(g.AdapterPayload)
			if g.WorkRevision != 1 || g.PlanRevision != 7 || g.BudgetGroupID != f.grant.BudgetGroupID || g.AttemptID == f.grant.AttemptID || next.Directory == old.Directory || filepath.Dir(next.Directory) != filepath.Dir(old.Directory) || len(filepath.Base(next.Directory)) > 80 {
				t.Fatal("retry lost scope, identity or fresh bounded directory")
			}
			next.Directory = old.Directory
			if !reflect.DeepEqual(old, next) || !reflect.DeepEqual(g.Input, f.grant.Input) {
				t.Fatal("retry changed profile/target/accepted candidate input")
			}
			var attempts int
			var used int64
			f.db.sql.QueryRow(`SELECT COUNT(*) FROM attempts WHERE task_id='review'`).Scan(&attempts)
			f.db.sql.QueryRow(`SELECT SUM(used_ms) FROM budget_runtime WHERE budget_group_id='review'`).Scan(&used)
			if attempts != 2 || used != 1234 {
				t.Fatalf("attempts=%d used=%d", attempts, used)
			}
			replay, err := f.db.PendingLaunches(ctx, f.host, 1)
			if err != nil || len(replay) != 1 || string(replay[0].AdapterPayload) != string(g.AdapterPayload) || replay[0].LaunchIntentHash != g.LaunchIntentHash {
				t.Fatalf("replay=%+v %v", replay, err)
			}
			if _, err = f.db.QueueRetry(ctx, f.spec); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.ClaimReady(ctx, f.host, 1, 2); err == nil {
				t.Fatal("duplicate dispatched again")
			}
			late := f.event
			late.EventID = "old-late-result"
			late.Kind = contract.EventResult
			late.Sequence = 7
			if _, err = f.db.CommitHostEvent(ctx, late); err != nil {
				t.Fatal(err)
			}
			var status, delivery string
			f.db.sql.QueryRow(`SELECT status FROM tasks WHERE id='review'`).Scan(&status)
			f.db.sql.QueryRow(`SELECT delivery_status FROM runtime_events WHERE event_id=?`, late.EventID).Scan(&delivery)
			if status != "running" || delivery != "internal" {
				t.Fatalf("old event affected new attempt: %s %s", status, delivery)
			}
		})
	}
}

func TestCandidateReviewResumeRejectedBeforeQueue(t *testing.T) {
	f := newReviewRetryFixture(t, contract.EventStopped)
	if err := f.db.QueueResume(context.Background(), "review", 1); err == nil {
		t.Fatal("candidate resume falsely queued")
	}
	var status string
	f.db.sql.QueryRow(`SELECT status FROM tasks WHERE id='review'`).Scan(&status)
	if status != "interrupted" {
		t.Fatal(status)
	}
}

func TestCandidateReviewRetryRejectsUnsafeStateAtomically(t *testing.T) {
	for _, name := range []string{"unknown", "active", "attempt_budget", "active_budget", "session", "writable", "other_provider", "missing_lock", "validation_not_accepted", "validation_tampered", "fallback", "rejected_review"} {
		t.Run(name, func(t *testing.T) {
			f := newReviewRetryFixture(t, contract.EventStopped)
			switch name {
			case "unknown":
				f.db.sql.Exec(`UPDATE segment_runtime SET status='unknown' WHERE segment_id=?`, f.grant.SegmentID)
			case "active":
				f.db.sql.Exec(`UPDATE segment_runtime SET status='running' WHERE segment_id=?`, f.grant.SegmentID)
			case "attempt_budget":
				f.db.sql.Exec(`UPDATE tasks SET max_attempts=1 WHERE id='review'`)
			case "active_budget":
				f.db.sql.Exec(`UPDATE budget_runtime SET used_ms=? WHERE segment_id=?`, defaultGroupActiveMS, f.grant.SegmentID)
			case "validation_not_accepted":
				f.db.sql.Exec(`DELETE FROM review_decisions WHERE task_id='validate'`)
			case "validation_tampered":
				if err := os.WriteFile(f.grant.Input.Event.Artifact.Path, []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "fallback":
				f.spec.UseNextFallback = true
			case "rejected_review":
				e := f.event
				e.Artifact = &contract.ArtifactRef{Path: "/private/reject", Size: 1, SHA256: strings.Repeat("b", 64)}
				b, _ := json.Marshal(e)
				f.db.sql.Exec(`UPDATE runtime_events SET body_json=? WHERE event_id=?`, string(b), e.EventID)
			default:
				p, _ := adapter.DecodeInvocationPayload(f.grant.AdapterPayload)
				switch name {
				case "session":
					p.SessionID = "existing"
					p.SessionKind = "session-id"
				case "writable":
					p.Profile.Permission = adapter.WorkspaceWrite
				case "other_provider":
					p.Provider = "antigravity-cli"
				case "missing_lock":
					p.ProviderLock = nil
				}
				b, _ := json.Marshal(p)
				f.db.sql.Exec(`UPDATE task_runtime SET adapter_payload=? WHERE task_id='review'`, string(b))
			}
			if _, err := f.db.QueueRetry(context.Background(), f.spec); err == nil {
				t.Fatal("unsafe retry accepted")
			}
			var count int
			f.db.sql.QueryRow(`SELECT COUNT(*) FROM retry_decisions`).Scan(&count)
			if count != 0 {
				t.Fatal("partial retry decision")
			}
			f.db.sql.QueryRow(`SELECT COUNT(*) FROM attempts WHERE task_id='review'`).Scan(&count)
			if count != 1 {
				t.Fatal("attempt budget changed")
			}
		})
	}
}

func TestCandidateReviewRetryRejectsChangedDecisionAndInput(t *testing.T) {
	f := newReviewRetryFixture(t, contract.EventStopped)
	ctx := context.Background()
	if _, err := f.db.QueueRetry(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	changed := f.spec
	changed.CommandID = "different"
	changed.NextAttemptNo = 3
	if _, err := f.db.QueueRetry(ctx, changed); err == nil {
		t.Fatal("changed decision replay accepted")
	}
	f.db.sql.Exec(`UPDATE review_decisions SET command_id='changed' WHERE task_id='validate'`)
	if _, err := f.db.ClaimReady(ctx, f.host, 1, 2); err == nil {
		t.Fatal("retry accepted changed validation input")
	}
	var count int
	f.db.sql.QueryRow(`SELECT COUNT(*) FROM attempts WHERE task_id='review'`).Scan(&count)
	if count != 1 {
		t.Fatal("failed bind consumed attempt")
	}
}

func TestCandidateReviewSummaryOffersRetryInsteadOfResume(t *testing.T) {
	f := newReviewRetryFixture(t, contract.EventStopped)
	summary, err := f.db.SummarizeRun(context.Background(), "review", "owner", f.control)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range summary.Tasks {
		if task.TaskID == "review" {
			if !slices.Contains(task.AllowedActions, "retry") || slices.Contains(task.AllowedActions, "resume") {
				t.Fatalf("actions=%v", task.AllowedActions)
			}
		}
	}
}

func TestCandidateReviewRetryRevokesReportsButKeepsDuplicateACK(t *testing.T) {
	f := newReviewRetryFixture(t, contract.EventStopped)
	ctx := context.Background()
	g := f.grant
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.RegisterReportCapability(ctx, contract.ReportCapabilityRegistration{CapabilityID: g.ReportCapabilityID, ProducerID: f.host, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, ExecutionEpoch: g.ExecutionEpoch, TokenHash: runtimeHash("token"), CapabilityDir: dir}); err != nil {
		t.Fatal(err)
	}
	report := ReportEventSpec{CapabilityID: g.ReportCapabilityID, Token: "token", EventID: "old-progress", Sequence: 1, Kind: "progress", Payload: json.RawMessage(`{"status":"resource_sample","sequence":1,"elapsed_ms":10}`)}
	first, err := f.db.CommitReportEvent(ctx, report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.QueueRetry(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	again, err := f.db.CommitReportEvent(ctx, report)
	if err != nil || first != again {
		t.Fatal("lost durable duplicate ACK")
	}
	report.EventID = "old-new-question"
	report.Sequence++
	report.Kind = "question"
	report.Payload = json.RawMessage(`{"question":"continue?"}`)
	if _, err = f.db.CommitReportEvent(ctx, report); err == nil {
		t.Fatal("old report cap accepted new question")
	}
	var count int
	f.db.sql.QueryRow(`SELECT COUNT(*) FROM report_events WHERE event_id='old-new-question'`).Scan(&count)
	if count != 0 {
		t.Fatal("old question persisted")
	}
}

func TestCandidateReviewRetryRejectsOldPendingQuestion(t *testing.T) {
	f := newReviewRetryFixture(t, contract.EventStopped)
	if _, err := f.db.sql.Exec(`INSERT INTO report_events(event_id,capability_id,sequence,kind,payload_hash,body_json,event_revision,action_slot,delivery_status,accounted_bytes,created_at) VALUES('old-question',?,1,'question','hash','{}',1,'question-slot','pending_session',2,'')`, f.grant.ReportCapabilityID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.QueueRetry(context.Background(), f.spec); err == nil {
		t.Fatal("retry swallowed old question")
	}
	var count int
	f.db.sql.QueryRow(`SELECT COUNT(*) FROM report_events WHERE event_id='old-question' AND delivery_status='pending_session'`).Scan(&count)
	if count != 1 {
		t.Fatal("old question changed")
	}
}

func TestCandidateReviewRetryThirdAttemptKeepsOriginalScopeAndBudget(t *testing.T) {
	f := newReviewRetryFixture(t, contract.EventStopped)
	ctx := context.Background()
	if _, err := f.db.QueueRetry(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	second, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	e := contract.Event{Version: 1, ProducerID: f.host, RunID: second.RunID, TaskID: second.TaskID, AttemptID: second.AttemptID, SegmentID: second.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: second.CommandID, EventID: "review-stopped-again", Sequence: 7, Kind: contract.EventStopped, PayloadHash: runtimeHash("second stopped")}
	if _, err = f.db.CommitHostEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	done := e
	done.EventID = "review-exited-again"
	done.Sequence = 8
	done.Kind = contract.EventExited
	zero := 0
	done.ExitCode = &zero
	done.ActiveMS = 2345
	if _, err = f.db.CommitHostEvent(ctx, done); err != nil {
		t.Fatal(err)
	}
	page, err := f.db.CollectPending(ctx, "review", "", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range page.Events {
		if v.EventID == e.EventID {
			e = v
		}
	}
	spec := RetrySpec{TaskID: "review", WorkRevision: 1, EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, SegmentID: e.SegmentID, NextAttemptNo: 3, CommandID: "retry-third"}
	if _, err = f.db.QueueRetry(ctx, spec); err != nil {
		t.Fatal(err)
	}
	third, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	p2, _ := adapter.DecodeInvocationPayload(second.AdapterPayload)
	p3, _ := adapter.DecodeInvocationPayload(third.AdapterPayload)
	if p2.Directory == p3.Directory || filepath.Dir(p2.Directory) != filepath.Dir(p3.Directory) || !reflect.DeepEqual(third.Input, f.grant.Input) {
		t.Fatal("third retry changed scope/input or reused directory")
	}
	var attempts int
	var used int64
	f.db.sql.QueryRow(`SELECT COUNT(*) FROM attempts WHERE task_id='review'`).Scan(&attempts)
	f.db.sql.QueryRow(`SELECT SUM(used_ms) FROM budget_runtime WHERE budget_group_id='review'`).Scan(&used)
	if attempts != 3 || used != 3579 {
		t.Fatalf("attempts=%d used=%d", attempts, used)
	}
}

func TestCandidateReviewRetryPreservesNondefaultSegmentCap(t *testing.T) {
	f := newReviewRetryFixture(t, contract.EventStopped)
	if _, err := f.db.sql.Exec(`UPDATE task_runtime SET max_active_ms=1000 WHERE task_id='review'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.QueueRetry(context.Background(), f.spec); err != nil {
		t.Fatal(err)
	}
	g, err := f.db.ClaimReady(context.Background(), f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if g.GrantedActiveMS != 1000 {
		t.Fatalf("grant=%d", g.GrantedActiveMS)
	}
	var used int64
	f.db.sql.QueryRow(`SELECT SUM(used_ms) FROM budget_runtime WHERE budget_group_id='review'`).Scan(&used)
	if used != 1234 {
		t.Fatalf("old charged=%d", used)
	}
}

func TestCandidateReviewRetryRejectsSharedGroupBeforeAnotherTaskCanClaimOrdinal(t *testing.T) {
	f := newReviewRetryFixture(t, contract.EventStopped, true)
	if _, err := f.db.QueueRetry(context.Background(), f.spec); err == nil {
		other, claimErr := f.db.ClaimReady(context.Background(), f.host, 1, 2)
		t.Fatalf("shared-group retry queued; next claimant=%s err=%v (can consume retry ordinal)", other.TaskID, claimErr)
	}
	var count int
	f.db.sql.QueryRow(`SELECT COUNT(*) FROM retry_decisions`).Scan(&count)
	if count != 0 {
		t.Fatal("partial decision")
	}
}

func TestCandidateReviewRetryFencesEarlierSegmentOfRetiredAttempt(t *testing.T) {
	f := newReviewRetryFixture(t, contract.EventStopped)
	ctx := context.Background()
	g := f.grant
	if _, err := f.db.sql.Exec(`INSERT INTO segments(id,attempt_id,segment_no,status,created_at) VALUES('earlier-segment',?,0,'exited','')`, g.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.sql.Exec(`INSERT INTO segment_runtime(segment_id,attempt_id,host_id,command_id,execution_epoch,slot_token,launch_intent_hash,reservation_id,status,outcome,deadline_unix_ms,created_at) SELECT 'earlier-segment',attempt_id,host_id,'earlier-command',execution_epoch,'earlier-slot','earlier-intent','earlier-reservation','exited','stopped',deadline_unix_ms,created_at FROM segment_runtime WHERE segment_id=?`, g.SegmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.sql.Exec(`INSERT INTO report_capabilities(capability_id,segment_id,producer_id,run_id,task_id,attempt_id,work_revision,execution_epoch,status,created_at) SELECT 'earlier-cap','earlier-segment',producer_id,run_id,task_id,attempt_id,work_revision,execution_epoch,'pending',created_at FROM report_capabilities WHERE capability_id=?`, g.ReportCapabilityID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.QueueRetry(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	late := f.event
	late.EventID = "earlier-late-result"
	late.SegmentID = "earlier-segment"
	late.CommandID = "earlier-command"
	late.Kind = contract.EventResult
	late.Sequence = 7
	if _, err := f.db.CommitHostEvent(ctx, late); err != nil {
		t.Fatal(err)
	}
	var delivery string
	f.db.sql.QueryRow(`SELECT delivery_status FROM runtime_events WHERE event_id=?`, late.EventID).Scan(&delivery)
	if delivery != "internal" {
		t.Fatalf("earlier segment event became %s", delivery)
	}
}

func TestCandidateReviewRetryFailsClosedIfAnotherRunTakesItsOrdinal(t *testing.T) {
	f := newReviewRetryFixture(t, contract.EventStopped)
	ctx := context.Background()
	if _, err := f.db.QueueRetry(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	receipt, err := f.db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "other-run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "other-origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "other-origin", HostGeneration: "other-generation", Executable: "/private/bin/orchestrator"}, Tasks: []TaskSpec{{ID: "other-task", RunID: "other-run", BudgetGroupID: "review", MaxAttempts: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := f.db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "other-origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "other-generation", PID: 20, Birth: "other-host", Executable: "/private/bin/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.db.ClaimReady(ctx, h, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	e := contract.Event{Version: 1, ProducerID: h, RunID: other.RunID, TaskID: other.TaskID, AttemptID: other.AttemptID, SegmentID: other.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: other.CommandID, EventID: "other-failed", Sequence: 1, Kind: contract.EventFailed, PayloadHash: runtimeHash("other failed")}
	if _, err = f.db.CommitHostEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	e.EventID = "other-exited"
	e.Sequence = 2
	e.Kind = contract.EventExited
	zero := 0
	e.ExitCode = &zero
	e.ActiveMS = 1
	if _, err = f.db.CommitHostEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	if g, err := f.db.ClaimReady(ctx, f.host, 1, 2); err == nil {
		p, _ := adapter.DecodeInvocationPayload(g.AdapterPayload)
		t.Fatalf("retry missed ledger after shared ordinal changed: directory=%s", p.Directory)
	}
	var count int
	f.db.sql.QueryRow(`SELECT COUNT(*) FROM attempts WHERE task_id='review'`).Scan(&count)
	if count != 1 {
		t.Fatal("failed retry binding consumed another attempt")
	}
}
