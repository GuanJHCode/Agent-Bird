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
	"testing"
)

func newBaseFixture(t *testing.T) (*DB, PlanSpec) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, e := Open(filepath.Join(dir, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	target := adapter.CandidateTarget{Worktree: "/private/repo", Ref: "refs/heads/main", BaseOID: strings.Repeat("a", 40)}
	var tasks []TaskSpec
	for i, id := range []string{"edit", "validate", "review", "integrate"} {
		task := TaskSpec{ID: id, RunID: "run", MaxAttempts: 3, MaxActiveMS: 30000, CompletionPolicy: "owner_review", BudgetGroupID: "old-" + id}
		p := adapter.InvocationPayload{Directory: "/private/work/" + id, Prompt: "original scope"}
		if i == 0 {
			p.CandidateWorkspace = &adapter.CandidateWorkspace{Version: 1, AutoDirectory: true, RepoRoot: target.Worktree, BaseOID: target.BaseOID, Paths: []string{"edit.txt"}}
			p.Provider = "claude-code"
			p.Profile = &adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: 30000}
		} else {
			task.Dependencies = []string{[]string{"edit", "validate", "review"}[i-1]}
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
	old := PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 7, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"}, Tasks: tasks}
	receipt, e := db.SubmitPlan(ctx, old)
	if e != nil {
		t.Fatal(e)
	}
	h, e := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "gen", PID: 10, Birth: "host", Executable: old.Host.Executable}, 1)
	if e != nil {
		t.Fatal(e)
	}
	f := &dagReworkFixture{db: db, dir: dir, host: h, grants: map[string]contract.LaunchCommand{}, artifacts: map[string]map[string]any{}}
	f.mutateArtifact = func(g contract.LaunchCommand, b map[string]any) {
		if g.TaskID == "validate" {
			binding := gitops.CandidateBinding{RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, PlanRevision: g.PlanRevision}
			p, _ := adapter.DecodeInvocationPayload(g.AdapterPayload)
			v := &newBaseValidation{Passed: true, CandidateOID: strings.Repeat("b", 40), TreeOID: strings.Repeat("e", 40), Binding: binding, Command: p.CandidateAction.Command, ExecutableSHA256: strings.Repeat("a", 64), OutputSHA256: strings.Repeat("b", 64)}
			b["validation"] = v
			raw, _ := json.Marshal(v)
			b["validation_digest"] = runtimeHash(string(raw))
		}
		if g.TaskID == "review" {
			b["validation"] = f.artifacts["validate"]["validation"]
		}
		if g.TaskID == "review" {
			p, _ := adapter.DecodeInvocationPayload(g.AdapterPayload)
			b["provider_lock"] = p.ProviderLock
		}
		if g.TaskID == "integrate" {
			b["validation"] = f.artifacts["review"]["validation"]
			b["validation_digest"] = f.artifacts["review"]["validation_digest"]
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
	next.Run.ID = "successor"
	next.Host.HostGeneration = "new-gen"
	next.Tasks = append([]TaskSpec(nil), tasks...)
	spec := &NewBaseReworkSpec{Version: 1, SourceIntegrationTaskID: "integrate", SourceEvent: reworkEventTuple(event), TaskMap: map[string]string{}, Feedback: "new generation feedback", Acceptance: "preserve existing behavior", CommandID: "generation-two"}
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
	next.LaunchID = "successor-launch"
	next.LaunchToken = "successor-host-token"
	next.ControlToken = "successor-control-token"
	return db, next
}
func TestNewBaseAdmissionMatrix(t *testing.T) {
	for _, name := range []string{"valid_replay", "source_auth", "pin", "path", "topology", "base", "source_tuple", "group_reuse", "quota", "commit_failure", "ordinary_context"} {
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
			if name != "valid_replay" {
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
			if e = db.sql.QueryRow(oracle).Scan(&rowBytes); e != nil {
				t.Fatal(e)
			}
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
