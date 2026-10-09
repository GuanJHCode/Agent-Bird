package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

func validationPolicyTask() TaskSpec {
	t := policyTask("validate", 3)
	t.ExecutionPolicy.Retry.Codes = []string{"validation_start_eagain", "validation_start_eintr"}
	t.AdapterPayload, _ = json.Marshal(adapter.InvocationPayload{Kind: "candidate", Directory: "/private/repo", CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "validate", AutoDirectory: true, SourceTask: "edit", Command: []string{"/usr/bin/true"}, CommandSHA256: strings.Repeat("a", 64), Target: adapter.CandidateTarget{Worktree: "/private/repo", Ref: "refs/heads/main", BaseOID: strings.Repeat("b", 40)}}})
	return t
}
func TestValidationRetryPolicyStageScope(t *testing.T) {
	for _, mode := range []string{"valid", "unpinned", "nonauto", "mixed", "probe", "integrate", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			task := validationPolicyTask()
			var p adapter.InvocationPayload
			json.Unmarshal(task.AdapterPayload, &p)
			switch mode {
			case "unpinned":
				p.CandidateAction.CommandSHA256 = ""
			case "nonauto":
				p.CandidateAction.AutoDirectory = false
			case "mixed":
				task.ExecutionPolicy.Retry.Codes = []string{"validation_start_eagain", "probe_start_eintr"}
			case "probe":
				task.ExecutionPolicy.Retry.Codes = []string{"probe_start_eagain"}
			case "integrate":
				p.CandidateAction.Operation = "integrate"
				p.CandidateAction.Command = nil
				p.CandidateAction.CommandSHA256 = ""
			case "fallback":
				task.FallbackPayloads = []json.RawMessage{[]byte(`{}`)}
			}
			task.AdapterPayload, _ = json.Marshal(p)
			_, e := normalizeExecutionTask(task)
			if (e == nil) != (mode == "valid") {
				t.Fatalf("mode=%s err=%v", mode, e)
			}
		})
	}
}

func validationStoreFixture(t *testing.T, vector ...bool) budgetBatchFixture {
	t.Helper()
	task := validationPolicyTask()
	task.Dependencies = []string{"edit"}
	if len(vector) > 0 && vector[0] {
		var payload adapter.InvocationPayload
		json.Unmarshal(task.AdapterPayload, &payload)
		payload.CandidateAction.DependencyInputVersion = 1
		task.AdapterPayload, _ = json.Marshal(payload)
	}
	task.ExecutionPolicy.Retry.BackoffMS = []int64{1, 2}
	producer := TaskSpec{ID: "edit", RunID: "run", MaxAttempts: 1}
	lock := &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: adapter.BinaryPin{Path: "/private/bin/claude", Version: "test", SHA256: strings.Repeat("a", 64)}}
	producer.AdapterPayload, _ = json.Marshal(adapter.InvocationPayload{Provider: string(adapter.ProviderClaude), Directory: "/private/edit", ProviderLock: lock, CandidateWorkspace: &adapter.CandidateWorkspace{Version: 1}})
	f := newPolicyFixture(t, []TaskSpec{producer, task})
	g := f.grant
	binding := gitops.CandidateBinding{RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, PlanRevision: g.PlanRevision}
	body, _ := json.Marshal(newBaseDelivery{Version: 1, Binding: binding, ProviderLock: lock, CandidateReceiptSHA256: strings.Repeat("d", 64), Candidate: gitops.CandidateReceipt{Binding: &binding, AttemptID: g.AttemptID, PlanRevision: int64(g.PlanRevision), RepoRoot: "/private/repo", BaseOID: strings.Repeat("b", 40), CandidateOID: strings.Repeat("c", 40), TreeOID: strings.Repeat("d", 40), PrivateRef: "refs/orchestrator/candidates/test", PrivateRefExpectedOID: strings.Repeat("c", 40), CommonDir: "/private/repo/.git", Worktree: "/private/edit", WorktreeIdentity: "/private/edit", RootIdentity: gitops.DirectoryIdentity{Device: 1, Inode: 2}, RepoRootIdentity: gitops.DirectoryIdentity{Device: 1, Inode: 3}, CommonDirIdentity: gitops.DirectoryIdentity{Device: 1, Inode: 4}, Changes: []gitops.CandidateChange{{Path: "f.go", Mode: "100644", BlobOID: strings.Repeat("e", 40)}}}})
	artifact := managedHostArtifactForTest(t, f.db, g, "terminal", 0, body)
	e := contract.Event{Version: 1, ProducerID: f.host, EventID: "edit-result", RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, ExecutionEpoch: 1, CommandID: g.CommandID, Sequence: 1, Kind: contract.EventResult, Artifact: artifact, PayloadHash: artifact.SHA256}
	ctx := context.Background()
	if _, err := f.db.CommitHostEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	done := e
	done.Kind = contract.EventExited
	done.EventID = "edit-exited"
	done.Sequence = 2
	zero := 0
	done.ExitCode = &zero
	done.ActiveMS = 10
	if _, err := f.db.CommitHostEvent(ctx, done); err != nil {
		t.Fatal(err)
	}
	page, err := f.db.CollectPending(ctx, "edit", "", 0, false)
	if err != nil || len(page.Events) != 1 {
		t.Fatal(page, err)
	}
	e = page.Events[0]
	if _, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "edit", WorkRevision: g.WorkRevision, EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, Decision: "accept", CommandID: "accept-edit"}); err != nil {
		t.Fatal(err)
	}
	f.grant, err = f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil || f.grant.TaskID != "validate" {
		t.Fatal(f.grant.TaskID, err)
	}
	f.sequence = 2
	f.send(t, contract.EventPrepared, 0)
	return f
}
func validationStoreRejection(t *testing.T, f *budgetBatchFixture, spoof bool) contract.Event {
	t.Helper()
	f.sequence++
	body := []byte(`{"version":1,"kind":"validation_start_failure","stage":"validation_start","code":"validation_start_eagain"}`)
	if spoof {
		body = []byte(`{"version":1,"kind":"validation_start_failure","stage":"validation_start","code":"validation_start_eintr"}`)
	}
	a := managedHostArtifactForTest(t, f.db, f.grant, "prelaunch", 0, body)
	g := f.grant
	var cleanup *contract.ValidationToolCleanup
	if g.ValidationTool != nil {
		v := g.ValidationTool
		cleanup = &contract.ValidationToolCleanup{Version: 1, ReservationID: v.ReservationID, ScopeSHA256: v.ScopeSHA256, ToolSHA256: v.ToolSHA256, JournalSHA256: runtimeHash("synthetic absent tool journal"), RemovedBytes: 0}
	}
	return contract.Event{ValidationToolCleanup: cleanup, Version: 1, ProducerID: f.host, EventID: "validation-rejected", RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, ExecutionEpoch: 1, CommandID: g.CommandID, Sequence: f.sequence, Kind: contract.EventLaunchRejected, Artifact: a, PayloadHash: a.SHA256, Failure: &contract.FailureFacts{Version: 1, Stage: "validation_start", Code: "validation_start_eagain", WorkerState: "not_started", ProbeState: "not_applicable"}}
}
func TestValidationRetryFrozenQueueClaimReplay(t *testing.T) {
	for _, mode := range []string{"valid_replay", "vector_replay", "source_drift", "adapter_drift", "host_drift", "missing_binding", "retired_binding", "spoof", "started", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			f := validationStoreFixture(t, mode == "vector_replay")
			ctx := context.Background()
			original := f.grant
			if mode == "started" {
				f.start(t)
			}
			e := validationStoreRejection(t, &f, mode == "spoof")
			if mode == "unknown" {
				e.Kind = contract.EventUnknown
				e.Failure = nil
				e.ValidationToolCleanup = nil
			}
			ack, err := f.db.CommitHostEvent(ctx, e)
			if mode == "spoof" || mode == "started" {
				if err == nil {
					t.Fatal("unsafe rejected facts accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var count int
			f.db.sql.QueryRow(`SELECT COUNT(*) FROM retry_decisions WHERE task_id='validate'`).Scan(&count)
			if mode == "unknown" {
				if count != 0 {
					t.Fatal("UNKNOWN queued")
				}
				return
			}
			if count != 1 {
				t.Fatalf("decisions=%d", count)
			}
			again, err := f.db.CommitHostEvent(ctx, e)
			if err != nil || again != ack {
				t.Fatal("replay ACK", again, err)
			}
			f.db.sql.Exec(`UPDATE retry_decisions SET eligible_at_unix_ms=1 WHERE task_id='validate'`)
			switch mode {
			case "source_drift":
				f.db.sql.Exec(`UPDATE task_runtime SET work_revision=2 WHERE task_id='edit'`)
			case "adapter_drift":
				f.db.sql.Exec(`UPDATE task_runtime SET adapter_payload=json_set(adapter_payload,'$.candidate_action.command_sha256',?) WHERE task_id='validate'`, strings.Repeat("b", 64))
			case "host_drift":
				f.db.sql.Exec(`UPDATE runtime_hosts SET pid=999 WHERE id=?`, f.host)
			case "missing_binding":
				f.db.sql.Exec(`DELETE FROM decision_attempt_bindings WHERE task_id='validate'`)
			case "retired_binding":
				f.db.sql.Exec(`UPDATE decision_attempt_bindings SET retired=1 WHERE task_id='validate'`)
			}

			next, err := f.db.ClaimReady(ctx, f.host, 1, 2)
			if mode != "valid_replay" && mode != "vector_replay" {
				if err == nil {
					t.Fatal("drift/missing authority claimed", mode)
				}
				return
			}
			if err != nil || next.AttemptID == original.AttemptID || !reflect.DeepEqual(next.Input, original.Input) || !reflect.DeepEqual(next.DependencyInputs, original.DependencyInputs) || string(next.AdapterPayload) != string(original.AdapterPayload) {
				t.Fatal("frozen successor", next, err)
			}
			pending, err := f.db.PendingLaunches(ctx, f.host, 1)
			if err != nil || len(pending) != 1 || pending[0].LaunchIntentHash != next.LaunchIntentHash {
				t.Fatal("bound replay", pending, err)
			}
		})
	}
}

func TestValidationRetryBoundMissingAuthority(t *testing.T) {
	for _, mode := range []string{"missing", "retired", "empty_decision"} {
		t.Run(mode, func(t *testing.T) {
			f := validationStoreFixture(t)
			ctx := context.Background()
			e := validationStoreRejection(t, &f, false)
			if _, err := f.db.CommitHostEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
			f.db.sql.Exec(`UPDATE retry_decisions SET eligible_at_unix_ms=1 WHERE task_id='validate'`)
			g, err := f.db.ClaimReady(ctx, f.host, 1, 2)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing":
				f.db.sql.Exec(`DELETE FROM decision_attempt_bindings WHERE attempt_id=?`, g.AttemptID)
			case "retired":
				if _, er := f.db.sql.Exec(`UPDATE decision_attempt_bindings SET retired=1 WHERE attempt_id=?`, g.AttemptID); er == nil {
					t.Fatal("consumed binding retirement bypassed schema CHECK")
				}
				return
			case "empty_decision":
				f.db.sql.Exec(`UPDATE retry_decisions SET revision_payload='' WHERE task_id='validate'`)
			}
			if _, err = f.db.PendingLaunches(ctx, f.host, 1); err == nil {
				t.Fatal("bound authority silently reconstructed", mode)
			}
		})
	}
}

func TestValidationRetryAdmissionRollbackAndProtection(t *testing.T) {
	for _, mode := range []string{"actual_commit", "quota", "stop", "budget", "hold"} {
		t.Run(mode, func(t *testing.T) {
			f := validationStoreFixture(t)
			ctx := context.Background()
			e := validationStoreRejection(t, &f, false)
			if mode == "stop" {
				if _, err := f.db.RequestStop(ctx, "validate", 1, "owner_stop_requested"); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "budget" {
				f.db.sql.Exec(`UPDATE tasks SET max_attempts=1 WHERE id='validate'`)
			}
			if mode == "actual_commit" {
				if _, err := f.db.sql.Exec(`CREATE TABLE validation_commit_fault(parent TEXT REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail_validation_commit AFTER INSERT ON retry_decisions BEGIN INSERT INTO validation_commit_fault VALUES('missing-parent'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "quota" {
				setContinuationQuotaRemaining(t, f.db, "run", 0)
			}
			before := durableIngressCommitSnapshot(t, f.db)
			var err error
			if mode == "actual_commit" {
				gate := &hostArtifactGate{}
				_, err = f.db.commitHostEvent(ctx, e, gate)
				if !errors.Is(err, errHostArtifactVerification) {
					t.Fatal("first intake gate", err)
				}
				gate.validation, err = f.db.prepareValidationRetry(ctx, e, gate.item)
				if err != nil {
					t.Fatal(err)
				}
				err = artifacts.VerifyHostSlotAndCommit(ctx, gate.item.root, gate.item.descriptor, gate.item.proof, func() error { gate.verified = true; _, er := f.db.commitHostEvent(ctx, e, gate); return er })
			} else {
				_, err = f.db.CommitHostEvent(ctx, e)
			}
			if mode == "quota" || mode == "actual_commit" {
				if err == nil || mode == "actual_commit" && !strings.Contains(err.Error(), "FOREIGN KEY") {
					t.Fatal("failed admission/actual COMMIT", err)
				}
				ext, er := sql.Open("sqlite", f.db.path)
				if er != nil {
					t.Fatal(er)
				}
				defer ext.Close()
				if before != durableIngressCommitSnapshot(t, f.db) || before != durableIngressCommitSnapshot(t, &DB{sql: ext}) {
					t.Fatal("rollback leaked terminal/queue/ledger")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var decisions int
			f.db.sql.QueryRow(`SELECT COUNT(*) FROM retry_decisions WHERE task_id='validate'`).Scan(&decisions)
			if mode != "hold" {
				if decisions != 0 {
					t.Fatal("stopped/exhausted queue", decisions)
				}
				return
			}
			var used, reserved int64
			var state string
			if er := f.db.sql.QueryRow(`SELECT status,used_ms FROM budget_runtime WHERE segment_id=?`, f.grant.SegmentID).Scan(&state, &used); er != nil || state != "settled" || used != 0 {
				t.Fatal("old settlement", state, used, er)
			}
			if er := f.db.sql.QueryRow(`SELECT reserved_active_ms FROM decision_attempt_bindings WHERE task_id='validate' AND attempt_id IS NULL AND retired=0`).Scan(&reserved); er != nil || reserved != 200 {
				t.Fatal("finite hold", reserved, er)
			}
			var frozenBytes int64
			if er := f.db.sql.QueryRow(`SELECT length(CAST(revision_payload AS BLOB)) FROM retry_decisions WHERE task_id='validate'`).Scan(&frozenBytes); er != nil || frozenBytes <= 0 {
				t.Fatal("source accounting", frozenBytes, er)
			}
		})
	}
}

func TestValidationStartFactsRequirePinnedActionAuthority(t *testing.T) {
	for _, mode := range []string{"ordinary", "missing_tool", "wrong_stage"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			if mode == "ordinary" {
				f := newPolicyFixture(t, []TaskSpec{policyTask("ordinary", 3)})
				e := validationStoreRejection(t, &f, false)
				if _, err := f.db.CommitHostEvent(ctx, e); err == nil {
					t.Fatal("ordinary grant accepted validation stage")
				}
				return
			}
			f := validationStoreFixture(t)
			e := validationStoreRejection(t, &f, false)
			if mode == "missing_tool" {
				if _, err := f.db.sql.Exec(`DELETE FROM validation_tool_reservations WHERE segment_id=?`, f.grant.SegmentID); err == nil {
					t.Fatal("schema allowed erasing held tool")
				}
				// Deliberate isolated DB corruption to exercise the independent intake gate.
				if _, err := f.db.sql.Exec(`DROP TRIGGER validation_tool_retained`); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.sql.Exec(`DELETE FROM validation_tool_reservations WHERE segment_id=?`, f.grant.SegmentID); err != nil {
					t.Fatal(err)
				}
				e.ValidationToolCleanup = nil
			}
			if mode == "wrong_stage" {
				e.Failure.Stage = "prelaunch_probe"
				e.Failure.ProbeState = "not_started"
			}
			if _, err := f.db.CommitHostEvent(ctx, e); err == nil {
				t.Fatal("missing/mixed validation authority accepted", mode)
			}
		})
	}
}
