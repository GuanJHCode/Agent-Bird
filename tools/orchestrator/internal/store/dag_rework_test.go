package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

func reworkVersionSpec(t *testing.T, spec ReworkSpec, version int) ReworkSpec {
	t.Helper()
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	wire["version"] = version
	raw, err = json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var out ReworkSpec
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func dagReworkState(t *testing.T, db *DB) string {
	t.Helper()
	var state string
	err := db.sql.QueryRow(`SELECT json_array((SELECT json_group_array(json_array(id,status,work_revision,adapter_payload)) FROM (SELECT t.id,t.status,r.work_revision,r.adapter_payload FROM tasks t JOIN task_runtime r ON r.task_id=t.id ORDER BY t.id)),(SELECT COUNT(*) FROM attempts),(SELECT COUNT(*) FROM retry_decisions),(SELECT COUNT(*) FROM decision_attempt_bindings),(SELECT COALESCE(SUM(used_ms),0) FROM budget_runtime),(SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''))`).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestDAGReworkRejectsUnknownVersionBeforeStateChange(t *testing.T) {
	for _, version := range []int{-1, 2} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			f := newReworkFixture(t)
			before := dagReworkState(t, f.db)
			spec := reworkVersionSpec(t, f.spec, version)
			if _, err := f.db.QueueRework(context.Background(), spec); err == nil || err.Error() != "invalid_rework_version" {
				t.Fatalf("unknown request version was interpreted as legacy authorization: %v", err)
			}
			if got := dagReworkState(t, f.db); got != before {
				t.Fatal("invalid version changed durable plan")
			}
		})
	}
}

func TestDAGReworkVersionOneCannotFallBackToLegacyProof(t *testing.T) {
	// This genuine legacy chain has no pinned provider lock/target identity.
	// Opting into a larger closure must require its new evidence contract.
	f := newReworkFixture(t)
	before := dagReworkState(t, f.db)
	spec := reworkVersionSpec(t, f.spec, 1)
	if _, err := f.db.QueueRework(context.Background(), spec); err == nil {
		t.Fatal("v1 silently used legacy four-node authorization")
	}
	if got := dagReworkState(t, f.db); got != before {
		t.Fatal("failed v1 request revised legacy chain")
	}
}

func TestDAGReworkRevisesOnlyDependentClosure(t *testing.T) {
	f := newDAGReworkFixture(t, adapter.ProviderClaude, adapter.ProviderClaude)
	ctx := context.Background()
	var before string
	if err := f.db.sql.QueryRow(`SELECT json_array(t.status,r.work_revision,r.adapter_payload,(SELECT COUNT(*) FROM attempts WHERE task_id=t.id),(SELECT SUM(used_ms) FROM budget_runtime WHERE budget_group_id=r.budget_group_id),(SELECT event_id FROM review_decisions WHERE task_id=t.id)) FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id='side'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	receipt, err := f.db.QueueRework(ctx, reworkVersionSpec(t, f.spec, 1))
	if err != nil {
		t.Fatalf("explicit closure authorization refused: %v", err)
	}
	if len(receipt.TaskRevisions) != 6 {
		t.Fatalf("closure revisions=%v", receipt.TaskRevisions)
	}
	for _, id := range []string{"edit", "validate", "audit", "review", "report", "integrate"} {
		if receipt.TaskRevisions[id] != 2 {
			t.Fatalf("missing descendant %s", id)
		}
	}
	if _, ok := receipt.TaskRevisions["side"]; ok {
		t.Fatal("independent producer revised")
	}
	var after string
	f.db.sql.QueryRow(`SELECT json_array(t.status,r.work_revision,r.adapter_payload,(SELECT COUNT(*) FROM attempts WHERE task_id=t.id),(SELECT SUM(used_ms) FROM budget_runtime WHERE budget_group_id=r.budget_group_id),(SELECT event_id FROM review_decisions WHERE task_id=t.id)) FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id='side'`).Scan(&after)
	if after != before {
		t.Fatalf("independent branch changed %s -> %s", before, after)
	}
	wire, _ := json.Marshal(receipt)
	var versioned struct {
		Version       int    `json:"version"`
		ClosureSHA256 string `json:"closure_sha256"`
	}
	json.Unmarshal(wire, &versioned)
	if versioned.Version != 1 || !validDigest(versioned.ClosureSHA256) {
		t.Fatalf("receipt does not bind complete closure: %s", wire)
	}
	next, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil || next.TaskID != "edit" || next.WorkRevision != 2 {
		t.Fatalf("closure root grant=%+v err=%v", next, err)
	}
}

func TestDAGReworkCompletesNewVectorAndReplays(t *testing.T) {
	for _, implementer := range []adapter.Provider{adapter.ProviderClaude, adapter.ProviderAGY} {
		for _, reviewer := range []adapter.Provider{adapter.ProviderClaude, adapter.ProviderAGY} {
			t.Run(string(implementer)+"/"+string(reviewer), func(t *testing.T) {
				f := newDAGReworkFixture(t, implementer, reviewer)
				ctx := context.Background()
				spec := reworkVersionSpec(t, f.spec, 1)
				receipt, err := f.db.QueueRework(ctx, spec)
				if err != nil {
					t.Fatal(err)
				}
				before := dagReworkState(t, f.db)
				spec.CommandID = "lost-ack-replay"
				again, err := f.db.QueueRework(ctx, spec)
				if err != nil || !sameJSON(receipt, again) || before != dagReworkState(t, f.db) {
					t.Fatalf("decision replay changed state: %+v %v", again, err)
				}
				var holds int
				var ms int64
				if err = f.db.sql.QueryRow(`SELECT COUNT(*),SUM(reserved_active_ms) FROM decision_attempt_bindings WHERE action_slot=? AND reserved_active_ms>0 AND attempt_id IS NULL`, spec.ActionSlot).Scan(&holds, &ms); err != nil || holds != 6 || ms <= 0 {
					t.Fatalf("holds=%d ms=%d %v", holds, ms, err)
				}
				for step := 0; step < 6; step++ {
					grant, err := f.db.ClaimReady(ctx, f.host, 1, 2)
					if err != nil {
						t.Fatal(err)
					}
					if grant.WorkRevision != 2 || grant.TaskID == "side" {
						t.Fatalf("wrong closure grant %+v", grant)
					}
					pending, err := f.db.PendingLaunches(ctx, f.host, 1)
					if err != nil || len(pending) != 1 || !sameJSON(pending[0], grant) {
						t.Fatalf("consumed replay %v %v", pending, err)
					}
					if grant.TaskID == "review" || grant.TaskID == "report" {
						if grant.DependencyInputs == nil || len(grant.DependencyInputs.Inputs) != 2 {
							t.Fatal("join vector absent")
						}
						for _, input := range grant.DependencyInputs.Inputs {
							want := 2
							if input.Event.TaskID == "side" {
								want = 1
							}
							if input.Event.WorkRevision != want {
								t.Fatalf("wrong revision vector %+v", grant.DependencyInputs)
							}
						}
					}
					f.finish(t, grant, false)
				}
				var completed, pending int
				f.db.sql.QueryRow(`SELECT COUNT(*) FROM tasks WHERE status='completed'`).Scan(&completed)
				f.db.sql.QueryRow(`SELECT COUNT(*) FROM decision_attempt_bindings WHERE attempt_id IS NULL AND retired=0`).Scan(&pending)
				if completed != 7 || pending != 0 {
					t.Fatalf("completed=%d pending=%d", completed, pending)
				}
			})
		}
	}
}

func TestDAGReworkMissingBindingNeverRepairs(t *testing.T) {
	for _, which := range []string{"root", "non_source_descendant", "consumed"} {
		t.Run(which, func(t *testing.T) {
			f := newDAGReworkFixture(t, adapter.ProviderClaude, adapter.ProviderClaude)
			ctx := context.Background()
			if _, err := f.db.QueueRework(ctx, reworkVersionSpec(t, f.spec, 1)); err != nil {
				t.Fatal(err)
			}
			id := "edit"
			if which == "non_source_descendant" {
				id = "report"
			}
			if which == "consumed" {
				if _, err := f.db.ClaimReady(ctx, f.host, 1, 2); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.db.sql.Exec(`DELETE FROM decision_attempt_bindings WHERE task_id=?`, id); err != nil {
				t.Fatal(err)
			}
			before := dagReworkState(t, f.db)
			var err error
			if which == "consumed" {
				_, err = f.db.PendingLaunches(ctx, f.host, 1)
			} else {
				_, err = f.db.ClaimReady(ctx, f.host, 1, 2)
			}
			if err == nil || dagReworkState(t, f.db) != before {
				t.Fatalf("missing binding repaired/dispatched: %v", err)
			}
		})
	}
}

func TestDAGReworkBoundaryAndProofDriftRefused(t *testing.T) {
	for _, which := range []string{"boundary_payload", "closure_payload", "dependencies", "capture"} {
		t.Run(which, func(t *testing.T) {
			f := newDAGReworkFixture(t, adapter.ProviderClaude, adapter.ProviderClaude)
			ctx := context.Background()
			if _, err := f.db.QueueRework(ctx, reworkVersionSpec(t, f.spec, 1)); err != nil {
				t.Fatal(err)
			}
			var err error
			switch which {
			case "boundary_payload":
				_, err = f.db.sql.Exec(`UPDATE task_runtime SET adapter_payload=json_set(adapter_payload,'$.prompt','changed') WHERE task_id='side'`)
			case "closure_payload":
				_, err = f.db.sql.Exec(`UPDATE task_runtime SET adapter_payload=json_set(adapter_payload,'$.prompt','changed') WHERE task_id='report'`)
			case "dependencies":
				_, err = f.db.sql.Exec(`UPDATE tasks SET dependencies_json='["side"]' WHERE id='report'`)
			case "capture":
				_, err = f.db.sql.Exec(`DROP TRIGGER dependency_inputs_pair_update`)
				if err == nil {
					_, err = f.db.sql.Exec(`UPDATE segment_runtime SET dependency_inputs_sha256=? WHERE segment_id=?`, strings.Repeat("0", 64), f.grants["review"].SegmentID)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before := dagReworkState(t, f.db)
			if _, err = f.db.ClaimReady(ctx, f.host, 1, 2); err == nil || before != dagReworkState(t, f.db) {
				t.Fatalf("drift dispatched: %v", err)
			}
		})
	}
}

func TestDAGReworkNonContentDependencyUsesOriginalCapture(t *testing.T) {
	f := newDAGReworkFixture(t, adapter.ProviderAGY, adapter.ProviderClaude, func(tasks []TaskSpec) []TaskSpec {
		var extra TaskSpec
		for i := range tasks {
			if tasks[i].ID == "validate" {
				extra = tasks[i]
				extra.ID = "side-validation"
				extra.Dependencies = []string{"side"}
				p, _ := adapter.DecodeInvocationPayload(extra.AdapterPayload)
				p.Directory = "/private/work/side-validation"
				p.CandidateAction.SourceTask = "side"
				extra.AdapterPayload, _ = json.Marshal(p)
			}
			if tasks[i].ID == "review" {
				p, _ := adapter.DecodeInvocationPayload(tasks[i].AdapterPayload)
				p.CandidateAction.SourceTask = "side-validation"
				tasks[i].Dependencies = []string{"side-validation", "validate"}
				tasks[i].AdapterPayload, _ = json.Marshal(p)
			}
		}
		return append(tasks, extra)
	})
	ctx := context.Background()
	receipt, err := f.db.QueueRework(ctx, reworkVersionSpec(t, f.spec, 1))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.TaskRevisions["side-validation"] != 0 || len(receipt.TaskRevisions) != 6 {
		t.Fatalf("boundary revised %+v", receipt)
	}
	g, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil || g.TaskID != "edit" {
		t.Fatalf("non-source claim %+v %v", g, err)
	}
}

func TestDAGReworkRejectsProviderOrScopeExpansionAtomically(t *testing.T) {
	for _, which := range []string{"disabled", "codex", "write_reviewer", "unpinned", "second_producer", "integrated", "oversize"} {
		t.Run(which, func(t *testing.T) {
			f := newDAGReworkFixture(t, adapter.ProviderClaude, adapter.ProviderAGY)
			ctx := context.Background()
			spec := reworkVersionSpec(t, f.spec, 1)
			var err error
			switch which {
			case "disabled":
				_, err = f.db.ProviderControl(ctx, "owner", string(adapter.ProviderAGY), "disable", nil)
			case "codex":
				_, err = f.db.sql.Exec(`UPDATE task_runtime SET adapter_payload=json_set(adapter_payload,'$.provider','codex') WHERE task_id='edit'`)
			case "write_reviewer":
				_, err = f.db.sql.Exec(`UPDATE task_runtime SET adapter_payload=json_set(adapter_payload,'$.profile.permission','workspace-write') WHERE task_id='review'`)
			case "unpinned":
				_, err = f.db.sql.Exec(`UPDATE task_runtime SET adapter_payload=json_remove(adapter_payload,'$.provider_lock') WHERE task_id='edit'`)
			case "second_producer":
				_, err = f.db.sql.Exec(`UPDATE tasks SET dependencies_json='["edit"]' WHERE id='side'`)
			case "integrated":
				_, err = f.db.sql.Exec(`INSERT INTO attempts(id,task_id,attempt_no,status,created_at) VALUES('old-integration','integrate',1,'completed','fixture')`)
			case "oversize":
				spec.Feedback = strings.Repeat("x", 16*1024)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := dagReworkState(t, f.db)
			if _, err = f.db.QueueRework(ctx, spec); err == nil || before != dagReworkState(t, f.db) {
				t.Fatalf("expanded scope admitted: %v", err)
			}
		})
	}
}

func TestDAGReworkTargetDriftRejectsAcceptWithoutReleasing(t *testing.T) {
	for _, which := range []string{"common_dir", "ref", "directory_identity", "root_common_dir"} {
		t.Run(which, func(t *testing.T) {
			f := newDAGReworkFixture(t, adapter.ProviderClaude, adapter.ProviderClaude)
			ctx := context.Background()
			if _, err := f.db.QueueRework(ctx, reworkVersionSpec(t, f.spec, 1)); err != nil {
				t.Fatal(err)
			}
			f.mutateArtifact = func(g contract.LaunchCommand, body map[string]any) {
				if g.WorkRevision != 2 {
					return
				}
				if which == "root_common_dir" && g.TaskID == "edit" {
					c := body["candidate"].(gitops.CandidateReceipt)
					c.CommonDir = "/private/other/.git"
					body["candidate"] = c
					return
				}
				if g.TaskID != "validate" {
					return
				}
				if which == "common_dir" {
					c := body["candidate"].(gitops.CandidateReceipt)
					c.CommonDir = "/private/other/.git"
					body["candidate"] = c
				} else {
					b := body["target"].(gitops.ReviewBinding)
					if which == "ref" {
						b.TargetRef = "refs/heads/other"
					} else {
						b.TargetDirectoryIdentity.Inode++
					}
					body["target"] = b
				}
			}
			target := "validate"
			if which == "root_common_dir" {
				target = "edit"
			}
			var event contract.Event
			for i := 0; i < 4; i++ {
				g, err := f.db.ClaimReady(ctx, f.host, 1, 2)
				if err != nil {
					t.Fatal(err)
				}
				if g.TaskID == target {
					event = f.publish(t, g, false, false)
					break
				}
				f.finish(t, g, false)
			}
			if event.TaskID != target {
				t.Fatal("target not dispatched")
			}
			before := dagReworkState(t, f.db)
			_, err := f.db.ReviewResult(ctx, ReviewSpec{TaskID: event.TaskID, WorkRevision: event.WorkRevision, EventID: event.EventID, EventRevision: event.EventRevision, EventHash: event.PayloadHash, ActionSlot: event.ActionSlot, Decision: "accept", CommandID: "drift-accept"})
			if err == nil || before != dagReworkState(t, f.db) {
				t.Fatalf("drift accept changed durable state: %v", err)
			}
			// A normal comparison error does not pretend that future holds retired.
			var count int
			f.db.sql.QueryRow(`SELECT COUNT(*) FROM decision_attempt_bindings WHERE attempt_id IS NULL AND retired=0`).Scan(&count)
			if count == 0 {
				t.Fatal("target mismatch silently retired remaining promises")
			}
		})
	}
}

func TestDAGReworkLegacyPositiveHoldAndRetirement(t *testing.T) {
	for _, end := range []string{"claim", "cancel", "disable"} {
		t.Run(end, func(t *testing.T) {
			f := newDAGReworkFixture(t, adapter.ProviderClaude, adapter.ProviderAGY, func(tasks []TaskSpec) []TaskSpec {
				for i := range tasks {
					if tasks[i].ID == "edit" || tasks[i].ID == "side" {
						tasks[i].BudgetGroupID = "shared"
					}
				}
				return tasks
			})
			ctx := context.Background()
			receipt, err := f.db.QueueRework(ctx, reworkVersionSpec(t, f.spec, 1))
			if err != nil {
				t.Fatal(err)
			}
			var hold int64
			if err = f.db.sql.QueryRow(`SELECT reserved_active_ms FROM decision_attempt_bindings WHERE task_id='edit'`).Scan(&hold); err != nil || hold <= 0 {
				t.Fatalf("missing legacy hold %d %v", hold, err)
			}
			tx, err := f.db.sql.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			// Two original attempts plus the promised root exhaust the shared group.
			if err = executionCapacity(ctx, tx, "side", nil); err != ErrAttemptLimit {
				t.Fatalf("sibling stole promised capacity: %v", err)
			}
			groupMS, _, err := pendingActiveHolds(ctx, tx, "shared", "side", nil)
			if err != nil || groupMS != hold {
				t.Fatalf("time not held %d %v", groupMS, err)
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			before := policyQuotaUsage(t, f.db)
			switch end {
			case "claim":
				g, err := f.db.ClaimReady(ctx, f.host, 1, 2)
				if err != nil || g.TaskID != "edit" || g.GrantedActiveMS != hold {
					t.Fatalf("own promise double counted: %+v %v", g, err)
				}
			case "cancel":
				if _, err = f.db.RequestStop(ctx, "edit", 2, "owner_stop_requested"); err != nil {
					t.Fatal(err)
				}
			case "disable":
				if _, err = f.db.ProviderControl(ctx, "owner", string(adapter.ProviderClaude), "disable", nil); err != nil {
					t.Fatal(err)
				}
			}
			var pending, history int
			var saved int64
			if err = f.db.sql.QueryRow(`SELECT COUNT(CASE WHEN attempt_id IS NULL AND retired=0 THEN 1 END),COUNT(*),SUM(reserved_active_ms) FROM decision_attempt_bindings`).Scan(&pending, &history, &saved); err != nil || history != 6 || saved <= 0 {
				t.Fatalf("lost hold history %d/%d %d %v", pending, history, saved, err)
			}
			if end != "claim" && (pending != 0 || policyQuotaUsage(t, f.db) != before) {
				t.Fatalf("retirement lost bytes/kept promise pending=%d", pending)
			}
			replay, err := f.db.QueueRework(ctx, reworkVersionSpec(t, f.spec, 1))
			if err != nil || !sameJSON(replay, receipt) {
				t.Fatalf("terminal replay changed receipt %v", err)
			}
		})
	}
}

func TestDAGReworkQuotaShortfallRollsBackWholeClosure(t *testing.T) {
	f := newDAGReworkFixture(t, adapter.ProviderClaude, adapter.ProviderClaude)
	ctx := context.Background()
	before := dagReworkState(t, f.db)
	initial := policyQuotaUsage(t, f.db)
	// An exact SQLite snapshot retains the same immutable artifact paths and event
	// widths, so the reference API admission establishes an exact byte boundary.
	path := filepath.Join(t.TempDir(), "reference.db")
	if _, err := f.db.sql.Exec(`VACUUM INTO ?`, path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	reference, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reference.Close() })
	if _, err = reference.QueueRework(ctx, reworkVersionSpec(t, f.spec, 1)); err != nil {
		t.Fatal(err)
	}
	growth := policyQuotaUsage(t, reference) - initial
	var payload string
	if err = reference.sql.QueryRow(`SELECT revision_payload FROM retry_decisions`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	cost := int64(len(payload))
	for _, id := range []string{"edit", "validate", "audit", "review", "report", "integrate"} {
		cost += decisionBindingBytes(f.spec.ActionSlot, id, "") + policyBindingHoldBytes
	}
	if growth != cost {
		t.Fatalf("unexpected cost %d want %d", growth, cost)
	}
	f.db.runControlLimit = initial + cost - 1
	if _, err = f.db.QueueRework(ctx, reworkVersionSpec(t, f.spec, 1)); err != CodeError("control_spool_full") {
		t.Fatalf("one-byte-short accepted %v", err)
	}
	if dagReworkState(t, f.db) != before {
		t.Fatal("quota failure leaked state")
	}
	f.db.runControlLimit++
	if _, err = f.db.QueueRework(ctx, reworkVersionSpec(t, f.spec, 1)); err != nil {
		t.Fatal(err)
	}
	if policyQuotaUsage(t, f.db) != initial+cost {
		t.Fatal("exact admission accounting differs")
	}
	if _, err = f.db.RequestStop(ctx, "edit", 2, "owner_stop_requested"); err != nil {
		t.Fatal(err)
	}
	if policyQuotaUsage(t, f.db) != initial+cost {
		t.Fatal("cancel erased durable accounting")
	}
}

func dagIndependentIntegration(targetPath, targetRef string) func([]TaskSpec) []TaskSpec {
	return func(tasks []TaskSpec) []TaskSpec {
		var additions []TaskSpec
		for _, task := range tasks {
			id, source := "", ""
			switch task.ID {
			case "validate":
				id, source = "side-validation", "side"
			case "review":
				id, source = "side-review", "side-validation"
			case "integrate":
				id, source = "side-integrate", "side-review"
			default:
				continue
			}
			task.ID = id
			task.Dependencies = []string{source}
			p, _ := adapter.DecodeInvocationPayload(task.AdapterPayload)
			p.Directory = "/private/work/" + id
			p.CandidateAction.SourceTask = source
			p.CandidateAction.Target.Worktree = targetPath
			p.CandidateAction.Target.Ref = targetRef
			task.AdapterPayload, _ = json.Marshal(p)
			additions = append(additions, task)
		}
		return append(tasks, additions...)
	}
}

func TestDAGReworkTargetReservationUsesRepositoryAndRef(t *testing.T) {
	for _, which := range []string{"ready_alias", "unknown_alias", "unknown_unresolved", "different_ref"} {
		t.Run(which, func(t *testing.T) {
			ref := "refs/heads/main"
			if which == "different_ref" {
				ref = "refs/heads/other"
			}
			f := newDAGReworkFixture(t, adapter.ProviderClaude, adapter.ProviderClaude, dagIndependentIntegration("/private/alias/../repo", ref))
			ctx := context.Background()
			if which == "unknown_alias" || which == "unknown_unresolved" {
				var active contract.LaunchCommand
				for i := 0; i < 4; i++ {
					g, err := f.db.ClaimReady(ctx, f.host, 1, 3)
					if err != nil {
						t.Fatal(err)
					}
					if g.TaskID == "side-integrate" {
						active = g
						break
					}
					f.finish(t, g, false)
				}
				if active.TaskID == "" {
					t.Fatal("independent integrate was not ready")
				}
				f.seq++
				e := contract.Event{Version: 1, ProducerID: f.host, EventID: active.SegmentID + "-unknown", RunID: active.RunID, TaskID: active.TaskID, AttemptID: active.AttemptID, SegmentID: active.SegmentID, WorkRevision: active.WorkRevision, ExecutionEpoch: 1, CommandID: active.CommandID, Sequence: f.seq, Kind: contract.EventUnknown, PayloadHash: strings.Repeat("c", 64)}
				if _, err := f.db.CommitHostEvent(ctx, e); err != nil {
					t.Fatal(err)
				}
			}
			if which == "unknown_unresolved" {
				if err := os.Remove(filepath.Join(f.dir, f.grants["side-review"].SegmentID+".json")); err != nil {
					t.Fatal(err)
				}
			}
			before := dagReworkState(t, f.db)
			_, err := f.db.QueueRework(ctx, reworkVersionSpec(t, f.spec, 1))
			if which == "different_ref" {
				if err != nil {
					t.Fatalf("different ref blocked: %v", err)
				}
				return
			}
			want := CodeError("rework_target_reserved")
			if which == "unknown_unresolved" {
				want = CodeError("rework_target_scope_unresolved")
			}
			if err != want || before != dagReworkState(t, f.db) {
				t.Fatalf("target exclusion failed %v want %v", err, want)
			}
		})
	}
}

func TestDAGReworkDispatchKeepsTargetReservationThroughUnknown(t *testing.T) {
	f := newConfiguredDAGReworkFixture(t, adapter.ProviderClaude, adapter.ProviderClaude, func(f *dagReworkFixture) {
		f.acceptResult = func(g contract.LaunchCommand) bool { return g.TaskID != "side-review" }
	}, dagIndependentIntegration("/private/alias/../repo", "refs/heads/main"))
	ctx := context.Background()
	if _, err := f.db.QueueRework(ctx, reworkVersionSpec(t, f.spec, 1)); err != nil {
		t.Fatal(err)
	}
	// The unrelated review becomes accepted after the closure reserves its target.
	page, err := f.db.CollectPending(ctx, "side-review", "", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	var event contract.Event
	for _, e := range page.Events {
		if e.Kind == contract.EventResult {
			event = e
		}
	}
	if _, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: event.TaskID, WorkRevision: event.WorkRevision, EventID: event.EventID, EventRevision: event.EventRevision, EventHash: event.PayloadHash, ActionSlot: event.ActionSlot, Decision: "accept", CommandID: "accept-independent-after-rework"}); err != nil {
		t.Fatal(err)
	}
	var integrated contract.LaunchCommand
	for i := 0; i < 6; i++ {
		g, err := f.db.ClaimReady(ctx, f.host, 1, 3)
		if err != nil {
			t.Fatal(err)
		}
		if g.TaskID == "side-integrate" {
			t.Fatal("later integration stole a queued target promise")
		}
		if g.TaskID == "integrate" {
			integrated = g
			break
		}
		f.finish(t, g, false)
	}
	if integrated.TaskID == "" {
		t.Fatal("promised integration did not dispatch")
	}
	f.seq++
	unknown := contract.Event{Version: 1, ProducerID: f.host, EventID: integrated.SegmentID + "-unknown", RunID: integrated.RunID, TaskID: integrated.TaskID, AttemptID: integrated.AttemptID, SegmentID: integrated.SegmentID, WorkRevision: integrated.WorkRevision, ExecutionEpoch: 1, CommandID: integrated.CommandID, Sequence: f.seq, Kind: contract.EventUnknown, PayloadHash: strings.Repeat("c", 64)}
	if _, err = f.db.CommitHostEvent(ctx, unknown); err != nil {
		t.Fatal(err)
	}
	before := dagReworkState(t, f.db)
	if _, err = f.db.ClaimReady(ctx, f.host, 1, 3); err != ErrNoReady || before != dagReworkState(t, f.db) {
		t.Fatalf("UNKNOWN lost exclusion: %v", err)
	}
}

func TestDAGReworkRejectsTwoIntegrationsForSameTarget(t *testing.T) {
	f := newDAGReworkFixture(t, adapter.ProviderClaude, adapter.ProviderClaude, func(tasks []TaskSpec) []TaskSpec {
		for _, task := range tasks {
			if task.ID == "integrate" {
				task.ID = "second-integrate"
				return append(tasks, task)
			}
		}
		return tasks
	})
	before := dagReworkState(t, f.db)
	if _, err := f.db.QueueRework(context.Background(), reworkVersionSpec(t, f.spec, 1)); err != CodeError("rework_target_reserved") || before != dagReworkState(t, f.db) {
		t.Fatalf("same target promised twice %v", err)
	}
}

func dagIndependentLegacyChain(tasks []TaskSpec) []TaskSpec {
	var additions []TaskSpec
	for _, task := range tasks {
		if task.ID != "edit" && task.ID != "validate" && task.ID != "review" && task.ID != "integrate" {
			continue
		}
		task.ID = "legacy-" + task.ID
		p, _ := adapter.DecodeInvocationPayload(task.AdapterPayload)
		p.Directory = "/private/work/" + task.ID
		if p.CandidateAction != nil {
			p.CandidateAction.SourceTask = "legacy-" + p.CandidateAction.SourceTask
			p.CandidateAction.DependencyInputVersion = 0
			task.Dependencies = []string{p.CandidateAction.SourceTask}
		}
		task.AdapterPayload, _ = json.Marshal(p)
		additions = append(additions, task)
	}
	return append(tasks, additions...)
}

func TestDAGReworkLaterLegacyDecisionCannotPoisonTargetPromise(t *testing.T) {
	for _, corrupt := range []string{"intact", "remove_version", "bad_json"} {
		t.Run(corrupt, func(t *testing.T) {
			f := newDAGReworkFixture(t, adapter.ProviderClaude, adapter.ProviderClaude, dagIndependentLegacyChain)
			ctx := context.Background()
			var review contract.Event
			for i := 0; i < 4; i++ {
				g, err := f.db.ClaimReady(ctx, f.host, 1, 3)
				if err != nil {
					t.Fatal(err)
				}
				if g.TaskID == "legacy-review" {
					review = f.finish(t, g, true)
					break
				}
				f.finish(t, g, false)
			}
			if review.TaskID == "" {
				t.Fatal("legacy review did not execute")
			}
			page, err := f.db.CollectPending(ctx, "legacy-edit", "", 0, false)
			if err != nil {
				t.Fatal(err)
			}
			var candidate contract.Event
			for _, e := range page.Events {
				if e.Kind == contract.EventResult {
					candidate = e
				}
			}
			legacy := ReworkSpec{TaskID: "legacy-edit", WorkRevision: 1, Candidate: reworkEventTuple(candidate), CandidateOID: strings.Repeat("b", 40), ReviewTaskID: "legacy-review", ReviewWorkRevision: 1, Review: reworkEventTuple(review), ActionSlot: review.ActionSlot, Feedback: "legacy repair", Acceptance: "legacy scope", CommandID: "legacy-rework"}
			if _, err = f.db.QueueRework(ctx, reworkVersionSpec(t, f.spec, 1)); err != nil {
				t.Fatal(err)
			}
			want := CodeError("rework_target_reserved")
			if corrupt != "intact" {
				query := `UPDATE retry_decisions SET revision_payload=json_remove(revision_payload,'$.request.version')`
				if corrupt == "bad_json" {
					query = `UPDATE retry_decisions SET revision_payload='{'`
				}
				if _, err = f.db.sql.Exec(query); err != nil {
					t.Fatal(err)
				}
				want = CodeError("rework_target_scope_unresolved")
			}
			before := dagReworkState(t, f.db)
			if _, err = f.db.QueueRework(ctx, legacy); err != want || before != dagReworkState(t, f.db) {
				t.Fatalf("later legacy poisoned reservation: %v", err)
			}
			if corrupt != "intact" {
				return
			}
			// The original closure still proceeds after the rejected competing command.
			g, err := f.db.ClaimReady(ctx, f.host, 1, 3)
			if err != nil || g.TaskID != "edit" {
				t.Fatalf("original closure blocked %+v %v", g, err)
			}
		})
	}
}

func TestDAGReworkLegacyDifferentTargetRemainsIndependent(t *testing.T) {
	for _, first := range []string{"v0", "v1"} {
		t.Run(first, func(t *testing.T) {
			f := newDAGReworkFixture(t, adapter.ProviderClaude, adapter.ProviderClaude, func(tasks []TaskSpec) []TaskSpec {
				tasks = dagIndependentLegacyChain(tasks)
				for i := range tasks {
					if strings.HasPrefix(tasks[i].ID, "legacy-") {
						p, _ := adapter.DecodeInvocationPayload(tasks[i].AdapterPayload)
						if p.CandidateAction != nil {
							p.CandidateAction.Target.Ref = "refs/heads/other"
							tasks[i].AdapterPayload, _ = json.Marshal(p)
						}
					}
				}
				return tasks
			})
			ctx := context.Background()
			var review contract.Event
			for i := 0; i < 4; i++ {
				g, err := f.db.ClaimReady(ctx, f.host, 1, 3)
				if err != nil {
					t.Fatal(err)
				}
				if g.TaskID == "legacy-review" {
					review = f.finish(t, g, true)
					break
				}
				f.finish(t, g, false)
			}
			page, err := f.db.CollectPending(ctx, "legacy-edit", "", 0, false)
			if err != nil {
				t.Fatal(err)
			}
			var candidate contract.Event
			for _, e := range page.Events {
				if e.Kind == contract.EventResult {
					candidate = e
				}
			}
			legacy := ReworkSpec{TaskID: "legacy-edit", WorkRevision: 1, Candidate: reworkEventTuple(candidate), CandidateOID: strings.Repeat("b", 40), ReviewTaskID: "legacy-review", ReviewWorkRevision: 1, Review: reworkEventTuple(review), ActionSlot: review.ActionSlot, Feedback: "legacy repair", Acceptance: "legacy scope", CommandID: "legacy-rework"}
			requests := []ReworkSpec{legacy, reworkVersionSpec(t, f.spec, 1)}
			if first == "v1" {
				requests[0], requests[1] = requests[1], requests[0]
			}
			for _, spec := range requests {
				if _, err = f.db.QueueRework(ctx, spec); err != nil {
					t.Fatalf("different target cannot coexist: %v", err)
				}
			}
			seen := map[string]bool{}
			for i := 0; i < 10; i++ {
				g, err := f.db.ClaimReady(ctx, f.host, 1, 3)
				if err != nil {
					t.Fatal(err)
				}
				if g.WorkRevision != 2 || seen[g.TaskID] {
					t.Fatalf("duplicate/wrong grant %+v", g)
				}
				seen[g.TaskID] = true
				f.finish(t, g, false)
			}
			if !seen["integrate"] || !seen["legacy-integrate"] {
				t.Fatal("independent integrations missing")
			}
		})
	}
}

func TestDAGReworkMalformedMarkerCannotEvadeBindingGuard(t *testing.T) {
	for _, corrupt := range []string{"remove_version", "bad_json"} {
		t.Run(corrupt, func(t *testing.T) {
			f := newDAGReworkFixture(t, adapter.ProviderClaude, adapter.ProviderClaude)
			ctx := context.Background()
			if _, err := f.db.QueueRework(ctx, reworkVersionSpec(t, f.spec, 1)); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.sql.Exec(`DELETE FROM decision_attempt_bindings`); err != nil {
				t.Fatal(err)
			}
			query := `UPDATE retry_decisions SET revision_payload=json_remove(revision_payload,'$.request.version')`
			if corrupt == "bad_json" {
				query = `UPDATE retry_decisions SET revision_payload='{'`
			}
			if _, err := f.db.sql.Exec(query); err != nil {
				t.Fatal(err)
			}
			before := dagReworkState(t, f.db)
			if _, err := f.db.ClaimReady(ctx, f.host, 1, 3); err == nil || before != dagReworkState(t, f.db) {
				t.Fatalf("malformed v1 became legacy initial grant %v", err)
			}
		})
	}
}

func TestDAGReworkCorruptPromiseCannotDisappearFromDispatch(t *testing.T) {
	for _, corrupt := range []string{"remove_version", "bad_json"} {
		t.Run(corrupt, func(t *testing.T) {
			f := newConfiguredDAGReworkFixture(t, adapter.ProviderClaude, adapter.ProviderClaude, func(f *dagReworkFixture) {
				f.acceptResult = func(g contract.LaunchCommand) bool { return g.TaskID != "side-review" }
			}, dagIndependentIntegration("/private/alias/../repo", "refs/heads/main"))
			ctx := context.Background()
			if _, err := f.db.QueueRework(ctx, reworkVersionSpec(t, f.spec, 1)); err != nil {
				t.Fatal(err)
			}
			if g, err := f.db.ClaimReady(ctx, f.host, 1, 3); err != nil || g.TaskID != "edit" {
				t.Fatalf("root grant %+v %v", g, err)
			}
			page, err := f.db.CollectPending(ctx, "side-review", "", 0, false)
			if err != nil {
				t.Fatal(err)
			}
			var e contract.Event
			for _, event := range page.Events {
				if event.Kind == contract.EventResult {
					e = event
				}
			}
			if _, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: e.TaskID, WorkRevision: e.WorkRevision, EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, Decision: "accept", CommandID: "later-independent-accept"}); err != nil {
				t.Fatal(err)
			}
			query := `UPDATE retry_decisions SET revision_payload=json_remove(revision_payload,'$.request.version')`
			if corrupt == "bad_json" {
				query = `UPDATE retry_decisions SET revision_payload='{'`
			}
			if _, err = f.db.sql.Exec(query); err != nil {
				t.Fatal(err)
			}
			before := dagReworkState(t, f.db)
			if _, err = f.db.ClaimReady(ctx, f.host, 1, 3); err != CodeError("rework_target_scope_unresolved") || before != dagReworkState(t, f.db) {
				t.Fatalf("corrupt promise disappeared: %v", err)
			}
		})
	}
}
