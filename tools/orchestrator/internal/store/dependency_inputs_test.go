package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// All prerequisites are real Store host events and owner decisions; neither a
// completed task flag nor a worker report stands in for accepted/exited input.
func dependencyFixture(t *testing.T) (*DB, string) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tasks := []TaskSpec{}
	for _, id := range []string{"z", "a", "m"} {
		tasks = append(tasks, TaskSpec{ID: id, RunID: "run", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"claude-code","candidate_workspace":{"version":1}}`)})
	}
	tasks = append(tasks, TaskSpec{ID: "join", RunID: "run", Dependencies: []string{"z", "a", "m"}, MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"kind":"candidate","candidate_action":{"version":1,"dependency_input_version":1,"operation":"validate","command":["/usr/bin/true"],"source_task":"z"}}`)})
	plan, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 7, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"}, Tasks: tasks})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "gen", PID: 10, Birth: "host", Executable: "/private/bin/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		c, err := db.ClaimReady(ctx, host, 1, 2)
		if err != nil {
			t.Fatal(err)
		}
		if c.TaskID == "join" {
			t.Fatal("join before acceptance")
		}
		e := contract.Event{Version: 1, ProducerID: host, EventID: c.TaskID + "-result", RunID: "run", TaskID: c.TaskID, AttemptID: c.AttemptID, SegmentID: c.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: c.CommandID, Sequence: int64(2*i + 1), Kind: contract.EventResult, PayloadHash: runtimeHash(c.TaskID), Artifact: &contract.ArtifactRef{ID: c.TaskID, Path: "/private/" + c.TaskID + ".json", Size: 1, SHA256: runtimeHash(c.TaskID)}}
		if _, err = db.CommitHostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
		done := e
		zero := 0
		done.EventID = c.TaskID + "-exited"
		done.Sequence++
		done.Kind = contract.EventExited
		done.ExitCode = &zero
		if _, err = db.CommitHostEvent(ctx, done); err != nil {
			t.Fatal(err)
		}
		page, err := db.CollectPending(ctx, c.TaskID, "", 0, false)
		if err != nil || len(page.Events) != 1 {
			t.Fatalf("page=%+v err=%v", page, err)
		}
		r := page.Events[0]
		if _, err = db.ReviewResult(ctx, ReviewSpec{TaskID: c.TaskID, WorkRevision: 1, EventID: r.EventID, EventRevision: r.EventRevision, EventHash: r.PayloadHash, ActionSlot: r.ActionSlot, Decision: "accept", CommandID: "accept-" + c.TaskID}); err != nil {
			t.Fatal(err)
		}
	}
	return db, host
}

func TestDependencyVectorCaptureReplayAndAccounting(t *testing.T) {
	db, host := dependencyFixture(t)
	ctx := context.Background()
	var before int64
	if err := db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	c, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if c.DependencyInputs == nil || len(c.DependencyInputs.Inputs) != 3 {
		t.Fatalf("vector=%+v", c.DependencyInputs)
	}
	for i, id := range []string{"a", "m", "z"} {
		if c.DependencyInputs.Inputs[i].Event.TaskID != id {
			t.Fatal("not canonical")
		}
	}
	var raw, digest string
	var after int64
	if err = db.sql.QueryRow(`SELECT dependency_inputs_json,dependency_inputs_sha256 FROM segment_runtime WHERE segment_id=?`, c.SegmentID).Scan(&raw, &digest); err != nil {
		t.Fatal(err)
	}
	if digest != runtimeHash(raw) {
		t.Fatal("capture hash")
	}
	if err = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if want := int64(16+len(raw)+len(digest)) + hostArtifactControlBytes(t, db, c.SegmentID); after-before != want {
		t.Fatalf("capture accounting delta=%d want=%d", after-before, want)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(db.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replay, err := reopened.PendingLaunches(ctx, host, 1)
	if err != nil || len(replay) != 1 {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	wire1, _ := json.Marshal(c)
	wire2, _ := json.Marshal(replay[0])
	if string(wire1) != string(wire2) {
		t.Fatal("reopened replay changed grant")
	}
}

func TestDependencyVectorCaptureImmutableAndCorruptionRejected(t *testing.T) {
	for _, kind := range []string{"clear", "corrupt_hash", "noncanonical", "adapter_drift", "revision_drift", "decision_drift"} {
		t.Run(kind, func(t *testing.T) {
			db, host := dependencyFixture(t)
			ctx := context.Background()
			c, err := db.ClaimReady(ctx, host, 1, 2)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "clear":
				if _, err = db.sql.Exec(`UPDATE segment_runtime SET dependency_inputs_json=NULL,dependency_inputs_sha256=NULL WHERE segment_id=?`, c.SegmentID); err == nil {
					t.Fatal("immutable capture could be erased")
				}
				return
			case "corrupt_hash", "noncanonical":
				if _, err = db.sql.Exec(`DROP TRIGGER dependency_inputs_pair_update`); err != nil {
					t.Fatal(err)
				}
				if kind == "corrupt_hash" {
					_, err = db.sql.Exec(`UPDATE segment_runtime SET dependency_inputs_sha256=? WHERE segment_id=?`, strings.Repeat("0", 64), c.SegmentID)
				} else {
					raw, _ := json.MarshalIndent(c.DependencyInputs, "", " ")
					_, err = db.sql.Exec(`UPDATE segment_runtime SET dependency_inputs_json=?,dependency_inputs_sha256=? WHERE segment_id=?`, string(raw), runtimeHash(string(raw)), c.SegmentID)
				}
			case "adapter_drift":
				_, err = db.sql.Exec(`UPDATE task_runtime SET adapter_payload='{}' WHERE task_id='a'`)
			case "revision_drift":
				_, err = db.sql.Exec(`UPDATE task_runtime SET work_revision=2 WHERE task_id='a'`)
			case "decision_drift":
				_, err = db.sql.Exec(`UPDATE review_decisions SET command_id='different' WHERE task_id='a'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.PendingLaunches(ctx, host, 1); err == nil {
				t.Fatal("changed dependency capture replayed")
			}
		})
	}
}

func TestDependencyVectorQuotaRollback(t *testing.T) {
	for _, mode := range []string{"quota", "disk"} {
		t.Run(mode, func(t *testing.T) {
			db, host := dependencyFixture(t)
			ctx := context.Background()
			if mode == "quota" {
				var used int64
				db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'`).Scan(&used)
				db.runControlLimit = used
			} else {
				db.storageAvailable = func(string) (uint64, error) { return db.minFreeBytes + db.sqliteWriteOverhead + 1, nil }
			}
			if _, err := db.ClaimReady(ctx, host, 1, 2); err == nil {
				t.Fatal("claim bypassed capture quota")
			}
			var n int
			db.sql.QueryRow(`SELECT COUNT(*) FROM attempts WHERE task_id='join'`).Scan(&n)
			if n != 0 {
				t.Fatalf("failed claim retained %d attempts", n)
			}
			var state string
			db.sql.QueryRow(`SELECT status FROM tasks WHERE id='join'`).Scan(&state)
			if state != "ready" {
				t.Fatalf("rollback state=%s", state)
			}
		})
	}
}

func dependencyClaimState(t *testing.T, db *DB) string {
	t.Helper()
	var state string
	err := db.sql.QueryRow(`SELECT json_array((SELECT COUNT(*) FROM attempts),(SELECT COUNT(*) FROM segments),(SELECT COUNT(*) FROM segment_runtime),(SELECT COUNT(*) FROM budget_runtime),(SELECT COUNT(*) FROM report_capabilities),(SELECT COUNT(*) FROM decision_attempt_bindings),(SELECT status FROM tasks WHERE id='join'),(SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'),(SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''))`).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestDependencyVectorExactQuotaAndSizeRollback(t *testing.T) {
	ctx := context.Background()
	reference, host := dependencyFixture(t)
	before := policyQuotaUsage(t, reference)
	c, err := reference.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	growth := policyQuotaUsage(t, reference) - before
	raw, _ := json.Marshal(c.DependencyInputs)
	if growth != int64(16+len(raw)+64)+hostArtifactControlBytes(t, reference, c.SegmentID) {
		t.Fatalf("wrong reference cost %d", growth)
	}
	for _, mode := range []string{"run", "global", "disk", "oversize", "unaccepted", "unexited"} {
		t.Run(mode, func(t *testing.T) {
			db, h := dependencyFixture(t)
			initial := policyQuotaUsage(t, db)
			switch mode {
			case "run":
				db.runControlLimit = initial + growth - 1
			case "global":
				db.userControlLimit = initial + growth - 1
			case "disk":
				db.storageAvailable = func(string) (uint64, error) {
					return db.minFreeBytes + db.sqliteWriteOverhead + db.criticalDiskReserve + uint64(4*contract.HostArtifactReservedBytes+growth) - 1, nil
				}
			case "oversize":
				payload, _ := json.Marshal(map[string]string{"prompt": strings.Repeat("x", 64*1024)})
				if _, err = db.sql.Exec(`UPDATE task_runtime SET adapter_payload=? WHERE task_id='a'`, string(payload)); err != nil {
					t.Fatal(err)
				}
			case "unaccepted":
				if _, err = db.sql.Exec(`DELETE FROM review_decisions WHERE task_id='a'`); err != nil {
					t.Fatal(err)
				}
			case "unexited":
				if _, err = db.sql.Exec(`UPDATE segment_runtime SET status='running' WHERE segment_id=(SELECT segment_id FROM runtime_events WHERE event_id='a-result')`); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := dependencyClaimState(t, db)
			_, err = db.ClaimReady(ctx, h, 1, 2)
			if err == nil {
				t.Fatal("unsafe join admitted")
			}
			if mode == "oversize" && err.Error() != "launch_message_too_large" {
				t.Fatalf("wrong size error %v", err)
			}
			if got := dependencyClaimState(t, db); got != snapshot {
				t.Fatalf("partial claim %s -> %s", snapshot, got)
			}
			switch mode {
			case "run":
				db.runControlLimit++
			case "global":
				db.userControlLimit++
			case "disk":
				db.storageAvailable = func(string) (uint64, error) {
					return db.minFreeBytes + db.sqliteWriteOverhead + db.criticalDiskReserve + uint64(4*contract.HostArtifactReservedBytes+growth), nil
				}
			default:
				return
			}
			granted, err := db.ClaimReady(ctx, h, 1, 2)
			if err != nil {
				t.Fatalf("exact limit rejected: %v", err)
			}
			if policyQuotaUsage(t, db) != initial+growth {
				t.Fatal("exact accounting mismatch")
			}
			if _, err = db.PendingLaunches(ctx, h, 1); err != nil {
				t.Fatal(err)
			}
			if policyQuotaUsage(t, db) != initial+growth {
				t.Fatal("replay double charged")
			}
			// Raw retention deletion exercises the capture trigger independently; keep
			// the original attempts/tasks while removing referencing capability/budget.
			if _, err = db.sql.Exec(`DELETE FROM report_capabilities WHERE segment_id=?; DELETE FROM budget_runtime WHERE segment_id=?; DELETE FROM segment_runtime WHERE segment_id=?`, granted.SegmentID, granted.SegmentID, granted.SegmentID); err != nil {
				t.Fatal(err)
			}
			if policyQuotaUsage(t, db) != initial+hostArtifactControlBytes(t, db, granted.SegmentID) {
				t.Fatal("capture deletion changed retained Host obligations or failed to refund capture")
			}
		})
	}
}

func TestDependencyVectorAdmissionRejectsUnsupportedEvidence(t *testing.T) {
	for _, version := range []int{-1, 0, 1, 2} {
		for _, policy := range []string{"", "owner_review", "artifact", "exit_success_fixture"} {
			t.Run(strings.Join([]string{string(rune('0' + version)), policy}, "/"), func(t *testing.T) {
				tasks := map[string]TaskSpec{"edit": {ID: "edit", AdapterPayload: json.RawMessage(`{"provider":"claude-code","candidate_workspace":{"version":1}}`)}, "side": {ID: "side", CompletionPolicy: policy}, "join": {ID: "join", Dependencies: []string{"side", "edit"}}}
				raw, _ := json.Marshal(map[string]any{"kind": "candidate", "candidate_action": map[string]any{"version": 1, "dependency_input_version": version, "operation": "validate", "source_task": "edit"}})
				join := tasks["join"]
				join.AdapterPayload = raw
				tasks["join"] = join
				err := validateCandidatePlan(tasks)
				want := version == 1 && (policy == "" || policy == "owner_review")
				if (err == nil) != want {
					t.Fatalf("version=%d policy=%s err=%v", version, policy, err)
				}
			})
		}
	}
}

func TestDependencyVectorDuplicatePlanRollsBackAdmission(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tasks := []TaskSpec{{ID: "edit", RunID: "run", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"claude-code","candidate_workspace":{"version":1}}`)}, {ID: "join", RunID: "run", MaxAttempts: 1, Dependencies: []string{"edit", "edit"}, AdapterPayload: json.RawMessage(`{"kind":"candidate","candidate_action":{"version":1,"dependency_input_version":1,"operation":"validate","source_task":"edit","command":["/usr/bin/true"]}}`)}}
	_, err = db.SubmitPlan(context.Background(), PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"}, Tasks: tasks})
	if err == nil || err.Error() != "candidate_dependency_inputs_invalid" {
		t.Fatalf("duplicate dependency admitted: %v", err)
	}
	var count int
	if err = db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM runs)+(SELECT COUNT(*) FROM tasks)+(SELECT COUNT(*) FROM host_launches)+(SELECT COUNT(*) FROM task_runtime)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial plan=%d %v", count, err)
	}
}
