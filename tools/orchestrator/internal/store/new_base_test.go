package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func newBaseFixture(t *testing.T) (*DB, PlanSpec) { return newBaseFixtureOnDB(t, nil, "") }
func newBaseFixtureOnDB(t *testing.T, existing *DB, prefix string) (*DB, PlanSpec) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db := existing
	var e error
	if db == nil {
		db, e = Open(filepath.Join(dir, "state.db"))
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { db.Close() })
	}
	target := adapter.CandidateTarget{Worktree: "/private/repo", Ref: "refs/heads/main", BaseOID: strings.Repeat("a", 40)}
	var tasks []TaskSpec
	for i, id := range []string{"edit", "validate", "review", "integrate"} {
		task := TaskSpec{ID: prefix + id, RunID: prefix + "run", MaxAttempts: 3, MaxActiveMS: 30000, CompletionPolicy: "owner_review", BudgetGroupID: prefix + "old-" + id}
		p := adapter.InvocationPayload{Directory: "/private/work/" + id, Prompt: "original scope"}
		if i == 0 {
			p.CandidateWorkspace = &adapter.CandidateWorkspace{Version: 1, AutoDirectory: true, RepoRoot: target.Worktree, BaseOID: target.BaseOID, Paths: []string{"edit.txt"}}
			p.Provider = "claude-code"
			p.Profile = &adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: 30000}
		} else {
			task.Dependencies = []string{prefix + []string{"edit", "validate", "review"}[i-1]}
			p.Kind = "candidate"
			p.CandidateAction = &adapter.CandidateAction{Version: 1, AutoDirectory: true, Operation: id, SourceTask: task.Dependencies[0], Target: target}
			if id == "validate" {
				p.CandidateAction.Command = []string{"/usr/bin/true"}
			}
			if id == "review" {
				p.Kind = ""
				p.Provider = "claude-code"
				p.Profile = &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 30000}
			}
			if id == "integrate" {
				p.Directory = target.Worktree
			}
		}
		if p.Provider != "" {
			p.ProviderLock = &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: adapter.BinaryPin{Path: "/private/bin/claude", Version: "fixture", SHA256: strings.Repeat("f", 64)}}
		}
		task.AdapterPayload, _ = json.Marshal(p)
		tasks = append(tasks, task)
	}
	old := PlanSpec{Run: RunSpec{ID: prefix + "run", ControllerThread: "owner", PlanRevision: 7, OriginContextID: prefix + "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: prefix + "origin", HostGeneration: prefix + "gen", Executable: "/private/bin/orchestrator"}, Tasks: tasks}
	receipt, e := db.SubmitPlan(ctx, old)
	if e != nil {
		t.Fatal(e)
	}
	h, e := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: prefix + "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: prefix + "gen", PID: 10, Birth: "host", Executable: old.Host.Executable}, 1)
	if e != nil {
		t.Fatal(e)
	}
	f := &dagReworkFixture{db: db, dir: dir, host: h, grants: map[string]contract.LaunchCommand{}, artifacts: map[string]map[string]any{}}
	f.mutateArtifact = func(g contract.LaunchCommand, b map[string]any) {
		if g.TaskID == prefix+"edit" {
			raw, _ := json.Marshal(b["candidate"])
			b["candidate_receipt_sha256"] = runtimeHash(string(raw))
		} else {
			source, _ := adapter.DecodeInvocationPayload(g.AdapterPayload)
			b["candidate_receipt_sha256"] = f.artifacts[source.CandidateAction.SourceTask]["candidate_receipt_sha256"]
		}
		if g.TaskID == prefix+"validate" {
			binding := gitops.CandidateBinding{RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, PlanRevision: g.PlanRevision}
			p, _ := adapter.DecodeInvocationPayload(g.AdapterPayload)
			v := &newBaseValidation{Passed: true, CandidateOID: strings.Repeat("b", 40), TreeOID: strings.Repeat("e", 40), Binding: binding, Command: p.CandidateAction.Command, ExecutableSHA256: strings.Repeat("a", 64), OutputSHA256: strings.Repeat("b", 64)}
			b["validation"] = v
			raw, _ := json.Marshal(v)
			b["validation_digest"] = runtimeHash(string(raw))
		}
		if g.TaskID == prefix+"review" {
			b["validation"] = f.artifacts[prefix+"validate"]["validation"]
		}
		if g.TaskID == prefix+"review" {
			p, _ := adapter.DecodeInvocationPayload(g.AdapterPayload)
			b["provider_lock"] = p.ProviderLock
		}
		if g.TaskID == prefix+"integrate" {
			b["validation"] = f.artifacts[prefix+"review"]["validation"]
			b["validation_digest"] = f.artifacts[prefix+"review"]["validation_digest"]
			b["integration"] = &gitops.IntegrationOutcome{State: gitops.IntegrationIntegrated, TargetHEAD: strings.Repeat("b", 40), CandidateOID: strings.Repeat("b", 40), TargetWorktree: target.Worktree}
		}
	}
	var event contract.Event
	for range tasks {
		g, e := db.ClaimReady(ctx, h, 1, 2)
		if e != nil {
			t.Fatal(e)
		}
		event = f.publish(t, g, false, true)
	}
	next := old
	next.Run.ID = prefix + "successor"
	next.Host.HostGeneration = prefix + "new-gen"
	next.Tasks = append([]TaskSpec(nil), tasks...)
	spec := &NewBaseReworkSpec{Version: 1, SourceIntegrationTaskID: prefix + "integrate", SourceEvent: reworkEventTuple(event), TaskMap: map[string]string{}, Feedback: "new generation feedback", Acceptance: "preserve existing behavior", CommandID: prefix + "generation-two"}
	for _, task := range tasks {
		spec.TaskMap[task.ID] = "next-" + task.ID
	}
	for i, task := range next.Tasks {
		p, _ := adapter.DecodeInvocationPayload(task.AdapterPayload)
		task.ID = spec.TaskMap[task.ID]
		task.RunID = next.Run.ID
		task.BudgetGroupID = "new-" + task.ID
		task.Dependencies = append([]string(nil), task.Dependencies...)
		for j, id := range task.Dependencies {
			task.Dependencies[j] = spec.TaskMap[id]
		}
		if p.CandidateWorkspace != nil {
			p.CandidateWorkspace.BaseOID = strings.Repeat("b", 40)
		}
		if p.CandidateAction != nil {
			p.CandidateAction.SourceTask = spec.TaskMap[p.CandidateAction.SourceTask]
			p.CandidateAction.Target.BaseOID = strings.Repeat("b", 40)
		}
		task.AdapterPayload, _ = json.Marshal(p)
		next.Tasks[i] = task
	}
	next.NewBaseRework = spec
	next.NewBaseAuthorization = &NewBaseAuthorization{ControllerThread: "owner", ControlToken: receipt.ControlToken}
	next.LaunchID = prefix + "successor-launch"
	next.LaunchToken = prefix + "successor-host-token"
	next.ControlToken = prefix + "successor-control-token"
	return db, next
}
func TestNewBaseAdmissionMatrix(t *testing.T) {
	for _, name := range []string{"valid_replay", "source_auth", "pin", "path", "topology", "base", "source_tuple", "group_reuse", "quota", "commit_failure", "ordinary_context", "owner_mode_replay"} {
		t.Run(name, func(t *testing.T) {
			db, p := newBaseFixture(t)
			before := newBaseSourceSnapshot(t, db)
			var quotaBefore int64
			if e := db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&quotaBefore); e != nil {
				t.Fatal(e)
			}
			switch name {
			case "source_auth":
				p.NewBaseAuthorization.ControlToken = "forged"
			case "pin", "path", "base":
				a, _ := adapter.DecodeInvocationPayload(p.Tasks[0].AdapterPayload)
				if name == "pin" {
					a.ProviderLock.Binary.SHA256 = strings.Repeat("1", 64)
				}
				if name == "path" {
					a.CandidateWorkspace.Paths = []string{"secret.txt"}
				}
				if name == "base" {
					a.CandidateWorkspace.BaseOID = strings.Repeat("9", 40)
				}
				p.Tasks[0].AdapterPayload, _ = json.Marshal(a)
			case "topology":
				p.Tasks[2].Dependencies = []string{"next-edit"}
			case "source_tuple":
				p.NewBaseRework.SourceEvent.EventHash = strings.Repeat("1", 64)
			case "group_reuse":
				p.Tasks[0].BudgetGroupID = "old-edit"
			case "quota":
				db.runControlLimit = 1
			case "commit_failure":
				if _, e := db.sql.Exec(`CREATE TABLE new_base_commit_guard(id TEXT PRIMARY KEY REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER new_base_commit_fail AFTER INSERT ON new_base_lineage BEGIN INSERT INTO new_base_commit_guard VALUES('absent'); END;`); e != nil {
					t.Fatal(e)
				}
			case "ordinary_context":
				a, _ := adapter.DecodeInvocationPayload(p.Tasks[0].AdapterPayload)
				a.CandidateWorkspace.NewBaseRework = &adapter.NewBaseReworkContext{Version: 1}
				p.Tasks[0].AdapterPayload, _ = json.Marshal(a)
				p.NewBaseRework = nil
				p.NewBaseAuthorization = nil
			}
			r, e := db.SubmitPlan(context.Background(), p)
			if name != "valid_replay" && name != "owner_mode_replay" {
				if e == nil {
					t.Fatal("invalid generation admitted")
				}
				var n int
				if e = db.sql.QueryRow(`SELECT COUNT(*) FROM runs WHERE id='successor'`).Scan(&n); e != nil || n != 0 {
					t.Fatalf("partial run=%d %v", n, e)
				}
				if newBaseSourceSnapshot(t, db) != before {
					t.Fatal("refusal mutated source")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if name == "owner_mode_replay" {
				p.Host.ExecutionOwnerMode = "supervised-v1"
				if _, e = db.SubmitPlan(context.Background(), p); !errors.Is(e, ErrConflict) {
					t.Fatalf("owner mode replay accepted: %v", e)
				}
				return
			}
			replay, e := db.SubmitPlan(context.Background(), p)
			if e != nil || !reflect.DeepEqual(r, replay) {
				t.Fatalf("replay %+v %v", replay, e)
			}
			if newBaseSourceSnapshot(t, db) != before {
				t.Fatal("admission mutated source")
			}
			var raw string
			if e = db.sql.QueryRow(`SELECT adapter_payload FROM task_runtime WHERE task_id='next-edit'`).Scan(&raw); e != nil {
				t.Fatal(e)
			}
			a, _ := adapter.DecodeInvocationPayload([]byte(raw))
			if a.CandidateWorkspace.NewBaseRework == nil || a.CandidateWorkspace.NewBaseRework.IntegratedHEAD != strings.Repeat("b", 40) || !strings.Contains(a.Prompt, p.NewBaseRework.Feedback) {
				t.Fatalf("missing brief: %s", raw)
			}
			// Independent SQL column-byte oracle plus exact newly retained adapter delta.
			var rowBytes, charged int64
			oracle := `SELECT 16+admission_bytes+length(CAST(run_id AS BLOB))+length(CAST(source_run_id AS BLOB))+length(CAST(source_task_id AS BLOB))+length(CAST(source_event_id AS BLOB))+length(CAST(source_event_hash AS BLOB))+length(CAST(command_id AS BLOB))+length(CAST(request_sha256 AS BLOB))+length(CAST(context_json AS BLOB))+length(CAST(launch_id AS BLOB))+length(CAST(launch_token_hash AS BLOB))+length(CAST(control_token_hash AS BLOB))+length(CAST(created_at AS BLOB)) FROM new_base_lineage WHERE run_id='successor'`
			if e = db.sql.QueryRow(oracle + ` UNION ALL SELECT 0 FROM new_base_target_reservations WHERE 0`).Scan(&rowBytes); e != nil {
				t.Fatal(e)
			}
			var targetBytes int64
			if e = db.sql.QueryRow(`SELECT length(CAST(run_id AS BLOB))+length(CAST(task_id AS BLOB))+length(CAST(target_key AS BLOB))+length(CAST(target_json AS BLOB))+length(CAST(status AS BLOB))+length(CAST(created_at AS BLOB))+length(CAST(updated_at AS BLOB)) FROM new_base_target_reservations WHERE run_id='successor'`).Scan(&targetBytes); e != nil {
				t.Fatal(e)
			}
			rowBytes += targetBytes
			if e = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&charged); e != nil || charged-quotaBefore != rowBytes {
				t.Fatalf("lineage quota delta=%d oracle=%d %v", charged-quotaBefore, rowBytes, e)
			}
			var delta int64
			for _, task := range p.Tasks {
				var stored string
				if e = db.sql.QueryRow(`SELECT adapter_payload FROM task_runtime WHERE task_id=?`, task.ID).Scan(&stored); e != nil {
					t.Fatal(e)
				}
				baseline, _ := adapter.DecodeInvocationPayload(task.AdapterPayload)
				canonical, _ := json.Marshal(baseline)
				delta += int64(len(stored) - len(canonical))
			}
			var reserved int64
			if e = db.sql.QueryRow(`SELECT admission_bytes FROM new_base_lineage WHERE run_id='successor'`).Scan(&reserved); e != nil || reserved != delta {
				t.Fatalf("payload added bytes=%d reserve=%d %v", delta, reserved, e)
			}
			changed := *p.NewBaseRework
			changed.Feedback = "altered"
			p.NewBaseRework = &changed
			if _, e = db.SubmitPlan(context.Background(), p); !errors.Is(e, ErrConflict) {
				t.Fatalf("changed replay: %v", e)
			}
			for _, q := range []string{`UPDATE new_base_lineage SET request_sha256='changed'`, `DELETE FROM new_base_lineage`} {
				if _, e = db.sql.Exec(q); e == nil {
					t.Fatal("mutable lineage")
				}
			}
			p.NewBaseAuthorization.ControlToken = "forged"
			if _, e = db.SubmitPlan(context.Background(), p); e == nil {
				t.Fatal("replay bypassed auth")
			}
		})
	}
}
func newBaseSourceSnapshot(t *testing.T, db *DB) string {
	t.Helper()
	var all []string
	queries := []string{
		`SELECT * FROM runs WHERE id='run'`, `SELECT * FROM tasks WHERE run_id='run'`, `SELECT r.* FROM task_runtime r JOIN tasks t ON t.id=r.task_id WHERE t.run_id='run'`,
		`SELECT a.* FROM attempts a JOIN tasks t ON t.id=a.task_id WHERE t.run_id='run'`, `SELECT s.* FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id WHERE t.run_id='run'`,
		`SELECT b.* FROM budget_runtime b JOIN segment_runtime s ON s.segment_id=b.segment_id JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id WHERE t.run_id='run'`,
		`SELECT e.* FROM runtime_events e JOIN tasks t ON t.id=e.task_id WHERE t.run_id='run'`, `SELECT d.* FROM review_decisions d JOIN tasks t ON t.id=d.task_id WHERE t.run_id='run'`,
		`SELECT * FROM run_control WHERE run_id='run'`, `SELECT * FROM storage_usage WHERE scope='run' AND scope_id='run'`}
	for _, q := range queries {
		rows, e := db.sql.Query(q)
		if e != nil {
			t.Fatal(e)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			v := make([]any, len(cols))
			dest := make([]any, len(cols))
			for i := range v {
				dest[i] = &v[i]
			}
			if e = rows.Scan(dest...); e != nil {
				t.Fatal(e)
			}
			raw, _ := json.Marshal(v)
			all = append(all, q+string(raw))
		}
		rows.Close()
	}
	return strings.Join(all, "\n")
}

func TestNewBaseTargetReservationMatrix(t *testing.T) {
	db, p := newBaseFixture(t)
	_, q := newBaseFixtureOnDB(t, db, "second-")
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, plan := range []PlanSpec{p, q} {
		wg.Add(1)
		go func(plan PlanSpec) { defer wg.Done(); _, err := db.SubmitPlan(ctx, plan); results <- err }(plan)
	}
	wg.Wait()
	close(results)
	successes, refusals := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, CodeError("new_base_target_reserved")) {
			refusals++
		} else {
			t.Fatalf("unexpected admission: %v", err)
		}
	}
	if successes != 1 || refusals != 1 {
		t.Fatalf("success=%d reserved=%d", successes, refusals)
	}
	var winner string
	if err := db.sql.QueryRow(`SELECT run_id FROM new_base_lineage`).Scan(&winner); err != nil {
		t.Fatal(err)
	}
	loser := q
	if winner == q.Run.ID {
		loser = p
	}
	ordinary := loser
	ordinary.NewBaseRework = nil
	ordinary.NewBaseAuthorization = nil
	if _, err := db.SubmitPlan(ctx, ordinary); !errors.Is(err, CodeError("new_base_target_reserved")) {
		t.Fatalf("ordinary admission bypassed hold: %v", err)
	}
	integrate := ordinary.Tasks[len(ordinary.Tasks)-1]
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := integrationDispatchAllowed(ctx, tx, readyTask{runID: ordinary.Run.ID, taskID: integrate.ID, payload: string(integrate.AdapterPayload), revision: 1})
	tx.Rollback()
	if allowed || !errors.Is(err, CodeError("new_base_target_reserved")) {
		t.Fatalf("ordinary dispatch bypassed hold: %v %v", allowed, err)
	}
	// Different refs are independently scoped and retain ordinary submission.
	for i, task := range ordinary.Tasks {
		a, _ := adapter.DecodeInvocationPayload(task.AdapterPayload)
		if a.CandidateAction != nil && a.CandidateAction.Operation != "compose" {
			a.CandidateAction.Target.Ref = "refs/heads/independent"
			ordinary.Tasks[i].AdapterPayload, _ = json.Marshal(a)
		}
	}
	if _, err = db.SubmitPlan(ctx, ordinary); err != nil {
		t.Fatalf("unrelated ref incorrectly deferred: %v", err)
	}
	var heldTask string
	if err = db.sql.QueryRow(`SELECT task_id FROM new_base_target_reservations WHERE status='reserved'`).Scan(&heldTask); err != nil {
		t.Fatal(err)
	}
	// An UNKNOWN/corrupt no-attempt state still cannot manufacture release proof.
	if _, err = db.sql.Exec(`UPDATE tasks SET status='unknown' WHERE id=?`, heldTask); err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`UPDATE new_base_target_reservations SET status='released' WHERE task_id=?`, heldTask); err == nil {
		t.Fatal("UNKNOWN released target")
	}
	if _, err = db.sql.Exec(`UPDATE tasks SET status='queued' WHERE id=?`, heldTask); err != nil {
		t.Fatal(err)
	}
	if _, err = db.RequestStop(ctx, heldTask, 1, "owner_stop_requested"); err != nil {
		t.Fatal(err)
	}
	var held int
	if err = db.sql.QueryRow(`SELECT COUNT(*) FROM new_base_target_reservations WHERE status='reserved'`).Scan(&held); err != nil || held != 0 {
		t.Fatalf("queued cancel leaked hold=%d %v", held, err)
	}
}

// Ordinary admission must establish queue order before a later generation can
// reserve the same target, even before its review input exists.
func TestNewBaseRespectsEarlierQueuedIntegration(t *testing.T) {
	for _, status := range []string{"queued", "resume_queued", "independent_ref"} {
		t.Run(status, func(t *testing.T) {
			db, next := newBaseFixture(t)
			_, ordinary := newBaseFixtureOnDB(t, db, "ordinary-")
			ordinary.NewBaseRework = nil
			ordinary.NewBaseAuthorization = nil
			if status == "independent_ref" {
				for i, task := range ordinary.Tasks {
					a, _ := adapter.DecodeInvocationPayload(task.AdapterPayload)
					if a.CandidateAction != nil {
						a.CandidateAction.Target.Ref = "refs/heads/independent"
						ordinary.Tasks[i].AdapterPayload, _ = json.Marshal(a)
					}
				}
			}
			if _, err := db.SubmitPlan(context.Background(), ordinary); err != nil {
				t.Fatal(err)
			}
			integrate := ordinary.Tasks[len(ordinary.Tasks)-1].ID
			var actual string
			if err := db.sql.QueryRow(`SELECT status FROM tasks WHERE id=?`, integrate).Scan(&actual); err != nil || actual != "queued" {
				t.Fatalf("ordinary integrate status=%s: %v", actual, err)
			}
			if status == "resume_queued" {
				// Exercise enumeration of the durable continuation queue state. The
				// continuation protocol itself is covered by the existing resume suite.
				if _, err := db.sql.Exec(`UPDATE tasks SET status='resume_queued' WHERE id=?`, integrate); err != nil {
					t.Fatal(err)
				}
			}
			if status == "independent_ref" {
				if _, err := db.SubmitPlan(context.Background(), next); err != nil {
					t.Fatalf("durably different ref incorrectly deferred: %v", err)
				}
				return
			}
			before := newBaseSourceSnapshot(t, db)
			if _, err := db.SubmitPlan(context.Background(), next); !errors.Is(err, CodeError("rework_target_scope_unresolved")) && !errors.Is(err, CodeError("rework_target_reserved")) {
				t.Fatalf("later generation overtook %s ordinary integrate: %v", status, err)
			}
			if after := newBaseSourceSnapshot(t, db); after != before {
				t.Fatal("refused successor mutated durable history")
			}
			var retained int
			if err := db.sql.QueryRow(`SELECT COUNT(*) FROM new_base_lineage`).Scan(&retained); err != nil || retained != 0 {
				t.Fatalf("refused successor retained lineage=%d: %v", retained, err)
			}
		})
	}
}

func TestNewBaseReservationIntegrity(t *testing.T) {
	for _, kind := range []string{"missing", "changed_key"} {
		t.Run(kind, func(t *testing.T) {
			db, next := newBaseFixture(t)
			if _, err := db.SubmitPlan(context.Background(), next); err != nil {
				t.Fatal(err)
			}
			tx, err := db.sql.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			// Simulate corrupt durable state without weakening production triggers.
			if kind == "missing" {
				_, err = tx.Exec(`DROP TRIGGER immutable_new_base_target_delete;DELETE FROM new_base_target_reservations`)
			} else {
				_, err = tx.Exec(`DROP TRIGGER immutable_new_base_target_identity;DROP TRIGGER new_base_target_release_proof;UPDATE new_base_target_reservations SET target_key=?`, strings.Repeat("f", 64))
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = activeNewBaseTargets(context.Background(), tx); !errors.Is(err, CodeError("new_base_target_reservation_invalid")) {
				t.Fatalf("corrupt %s ticket was ignored: %v", kind, err)
			}
		})
	}
}
