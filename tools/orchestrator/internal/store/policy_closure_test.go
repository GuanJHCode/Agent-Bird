package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestPolicyTerminalRejectionClosesOnlyItsDependents(t *testing.T) {
	for _, mode := range []string{"no-retry", "unlisted-code", "unclassified", "backoff-exhausted"} {
		t.Run(mode, func(t *testing.T) {
			a := policyTask("a", 3)
			if mode == "no-retry" {
				a.ExecutionPolicy.Retry = nil
			}
			if mode == "unlisted-code" {
				a.ExecutionPolicy.Retry.Codes = []string{"probe_start_eintr"}
			}
			if mode == "backoff-exhausted" {
				a.ExecutionPolicy.Retry.BackoffMS = []int64{1}
			}
			b, c, x, y := policyTask("b", 3), policyTask("c", 3), policyTask("x", 3), policyTask("y", 3)
			b.Dependencies, c.Dependencies, y.Dependencies = []string{"a"}, []string{"b"}, []string{"x"}
			f := newPolicyFixture(t, []TaskSpec{a, b, c, x, y})
			ctx := context.Background()
			// An unrelated retryable failure must not become terminal just because a rejects.
			if _, err := f.db.sql.Exec(`UPDATE tasks SET status='failed' WHERE id='x'`); err != nil {
				t.Fatal(err)
			}
			if mode == "backoff-exhausted" {
				if _, err := f.db.CommitHostEvent(ctx, policyRejection(t, &f, "probe_start_eagain")); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.sql.Exec(`UPDATE retry_decisions SET eligible_at_unix_ms=1`); err != nil {
					t.Fatal(err)
				}
				g, err := f.db.ClaimReady(ctx, f.host, 1, 2)
				if err != nil {
					t.Fatal(err)
				}
				f.grant = g
			}
			e := policyRejection(t, &f, "probe_start_eagain")
			if mode == "unclassified" {
				e.Failure = nil
			}
			if _, err := f.db.CommitHostEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
			for id, want := range map[string]string{"a": "failed", "b": "blocked_dependency", "c": "blocked_dependency", "x": "failed", "y": "queued"} {
				var got string
				if err := f.db.sql.QueryRow(`SELECT status FROM tasks WHERE id=?`, id).Scan(&got); err != nil || got != want {
					t.Errorf("%s status=%s want=%s err=%v", id, got, want, err)
				}
			}
			before, err := f.db.ReleasableTasks(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range before {
				if r.TaskID == "a" {
					t.Error("released before event ACK")
				}
			}
			page, err := f.db.CollectPending(ctx, "a", "", 0, false)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range page.Events {
				ackEvent(t, f.db, "a", event, "handled")
			}
			after, err := f.db.ReleasableTasks(ctx)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, r := range after {
				found = found || r.TaskID == "a"
			}
			if !found {
				t.Error("terminal rejected task remains pinned after ACK")
			}
		})
	}
}

func TestPolicyPendingTimeCannotBeStolen(t *testing.T) {
	a, b := policyTask("a", 2), policyTask("b", 2)
	for _, task := range []*TaskSpec{&a, &b} {
		task.BudgetGroupID = "shared"
		task.MaxActiveMS = 200
		task.ExecutionPolicy.GroupMaxAttempts = 4
		task.ExecutionPolicy.GroupMaxActiveMS = 300
		task.ExecutionPolicy.TaskMaxActiveMS = 300
	}
	f := newPolicyFixture(t, []TaskSpec{a, b})
	ctx := context.Background()
	if _, err := f.db.CommitHostEvent(ctx, policyRejection(t, &f, "probe_start_eagain")); err != nil {
		t.Fatal(err)
	}
	g, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if g.TaskID != "b" || g.GrantedActiveMS != 100 {
		t.Fatalf("sibling stole promised time: %+v", g)
	}
	f.grant = g
	f.start(t)
	f.send(t, contract.EventFailed, 0)
	f.send(t, contract.EventExited, 100)
	if _, err = f.db.sql.Exec(`UPDATE retry_decisions SET eligible_at_unix_ms=1`); err != nil {
		t.Fatal(err)
	}
	path := f.db.path
	f.db.Close()
	f.db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	g, err = f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil || g.TaskID != "a" || g.GrantedActiveMS != 200 {
		t.Fatalf("durable promise lost: %+v %v", g, err)
	}
}

func TestPolicyCancelRetiresPendingPromiseAndFreesTime(t *testing.T) {
	a, b := policyTask("a", 2), policyTask("b", 2)
	for _, task := range []*TaskSpec{&a, &b} {
		task.BudgetGroupID = "shared"
		task.MaxActiveMS = 200
		task.ExecutionPolicy.GroupMaxAttempts = 2
		task.ExecutionPolicy.GroupMaxActiveMS = 200
		task.ExecutionPolicy.TaskMaxActiveMS = 200
	}
	f := newPolicyFixture(t, []TaskSpec{a, b})
	ctx := context.Background()
	if _, err := f.db.CommitHostEvent(ctx, policyRejection(t, &f, "probe_start_eagain")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.RequestStop(ctx, "a", 1, "owner_stop_requested"); err != nil {
		t.Fatal(err)
	}
	g, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil || g.TaskID != "b" || g.GrantedActiveMS != 200 {
		t.Fatalf("cancel retained capacity: %+v %v", g, err)
	}
	var history int
	if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM retry_decisions WHERE task_id='a'`).Scan(&history); err != nil || history != 1 {
		t.Fatalf("decision history lost: %d %v", history, err)
	}
	if _, err = f.db.RequestStop(ctx, "a", 1, "owner_stop_requested"); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancelled task resurrected: %v", err)
	}
}

func TestPolicyProviderDisableRetiresRetryAndDependencies(t *testing.T) {
	for _, when := range []string{"before-rejection", "during-backoff"} {
		t.Run(when, func(t *testing.T) {
			a, b := policyTask("a", 3), policyTask("b", 3)
			a.AdapterPayload = json.RawMessage(`{"provider":"claude-code"}`)
			b.Dependencies = []string{"a"}
			f := newPolicyFixture(t, []TaskSpec{a, b})
			ctx := context.Background()
			e := policyRejection(t, &f, "probe_start_eagain")
			if when == "during-backoff" {
				if _, err := f.db.CommitHostEvent(ctx, e); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.db.ProviderControl(ctx, "owner", "claude-code", "disable", nil); err != nil {
				t.Fatal(err)
			}
			if when == "before-rejection" {
				if _, err := f.db.CommitHostEvent(ctx, e); err != nil {
					t.Fatal(err)
				}
			}
			var child string
			if err := f.db.sql.QueryRow(`SELECT status FROM tasks WHERE id='b'`).Scan(&child); err != nil || child != "blocked_dependency" {
				t.Fatalf("child=%s %v", child, err)
			}
			if _, err := f.db.ProviderControl(ctx, "owner", "claude-code", "enable", nil); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.ClaimReady(ctx, f.host, 1, 2); !errors.Is(err, ErrNoReady) {
				t.Fatalf("disable resurrected retry after enable: %v", err)
			}
			page, err := f.db.CollectPending(ctx, "a", "", 0, false)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range page.Events {
				ackEvent(t, f.db, "a", e, "handled")
			}
			hosts, err := f.db.RetirableHosts(ctx)
			if err != nil || len(hosts) != 1 {
				t.Fatalf("pending promise retained host: %v %v", hosts, err)
			}
			if err = f.db.RetireHost(ctx, f.host); err != nil {
				t.Fatal(err)
			}
			idle, err := f.db.CompletelyIdle(ctx)
			if err != nil || !idle {
				t.Fatalf("not idle: %v %v", idle, err)
			}
		})
	}
}

func TestPolicyReworkReservesEachBindingAtomically(t *testing.T) {
	for _, total := range []int64{1200, 1300} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			f := newReworkFixtureWithTasks(t, nil, func(tasks []TaskSpec) {
				for i := range tasks {
					tasks[i].BudgetGroupID = "shared"
					tasks[i].MaxActiveMS = 200
					tasks[i].ExecutionPolicy = &contract.ExecutionPolicy{Version: 1, GroupMaxAttempts: 8, GroupMaxActiveMS: total, TaskMaxActiveMS: 400, SegmentMaxActiveMS: 200}
				}
			})
			ctx := context.Background()
			receipt, err := f.db.QueueRework(ctx, f.spec)
			if total == 1200 {
				if !errors.Is(err, CodeError("budget_exhausted")) {
					t.Fatalf("oversold rework: %v", err)
				}
				var decisions, bindings, rev int
				if err = f.db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM retry_decisions),(SELECT COUNT(*) FROM decision_attempt_bindings),(SELECT work_revision FROM task_runtime WHERE task_id='edit')`).Scan(&decisions, &bindings, &rev); err != nil || decisions != 0 || bindings != 0 || rev != 1 {
					t.Fatalf("partial rework committed: %d/%d/%d %v", decisions, bindings, rev, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for id, want := range map[string]int64{"edit": 200, "validate": 200, "review": 200, "integrate": 100} {
				var hold int64
				if err = f.db.sql.QueryRow(`SELECT reserved_active_ms FROM decision_attempt_bindings WHERE task_id=? AND attempt_id IS NULL AND retired=0`, id).Scan(&hold); err != nil || hold != want {
					t.Errorf("%s hold=%d want=%d %v", id, hold, want, err)
				}
			}
			replay, err := f.db.QueueRework(ctx, f.spec)
			if err != nil || replay.RevisionSHA256 != receipt.RevisionSHA256 {
				t.Fatalf("replay changed %v", err)
			}
			if _, err = f.db.RequestStop(ctx, "edit", 2, "owner_stop_requested"); err != nil {
				t.Fatal(err)
			}
			var pending, historical int
			if err = f.db.sql.QueryRow(`SELECT COUNT(CASE WHEN retired=0 AND attempt_id IS NULL THEN 1 END),COUNT(*) FROM decision_attempt_bindings`).Scan(&pending, &historical); err != nil || pending != 0 || historical != 4 {
				t.Fatalf("cancelled rework retained holds/lost history: %d/%d %v", pending, historical, err)
			}
		})
	}
}

func TestPolicyOwnerStopBeforeRejectionCannotRestart(t *testing.T) {
	f := newPolicyFixture(t, []TaskSpec{policyTask("a", 3)})
	ctx := context.Background()
	if _, err := f.db.RequestStop(ctx, "a", 1, "owner_stop_requested"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.CommitHostEvent(ctx, policyRejection(t, &f, "probe_start_eagain")); err != nil {
		t.Fatal(err)
	}
	var promises int
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM retry_decisions`).Scan(&promises); err != nil || promises != 0 {
		t.Fatalf("owner stop created retry=%d %v", promises, err)
	}
}

func TestPolicyHoldCannotGrowAfterAnotherAttemptRefunds(t *testing.T) {
	a, b := policyTask("a", 2), policyTask("b", 2)
	for _, task := range []*TaskSpec{&a, &b} {
		task.BudgetGroupID = "shared"
		task.MaxActiveMS = 200
		task.ExecutionPolicy.GroupMaxAttempts = 4
		task.ExecutionPolicy.GroupMaxActiveMS = 300
		task.ExecutionPolicy.TaskMaxActiveMS = 300
	}
	f := newPolicyFixture(t, []TaskSpec{a, b})
	ctx := context.Background()
	first := f.grant
	second, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil || second.GrantedActiveMS != 100 {
		t.Fatalf("second=%+v %v", second, err)
	}
	f.grant = second
	if _, err = f.db.CommitHostEvent(ctx, policyRejection(t, &f, "probe_start_eagain")); err != nil {
		t.Fatal(err)
	}
	f.grant = first
	f.start(t)
	f.send(t, contract.EventFailed, 0)
	f.send(t, contract.EventExited, 1)
	if _, err = f.db.sql.Exec(`UPDATE retry_decisions SET eligible_at_unix_ms=1`); err != nil {
		t.Fatal(err)
	}
	g, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil || g.TaskID != "b" || g.GrantedActiveMS != 100 {
		t.Fatalf("saved hold grew after refund: %+v %v", g, err)
	}
}

func TestPolicyRejectsUnsupportedFallbackBeforeAdmission(t *testing.T) {
	for name, raw := range map[string]string{
		"candidate": `{"kind":"candidate","candidate_action":{"version":1,"operation":"validate","source_task":"source","command":["/usr/bin/true"]}}`,
		"review":    `{"provider":"claude-code","candidate_action":{"version":1,"operation":"review","source_task":"source"}}`,
		"gitops":    `{"kind":"gitops"}`,
	} {
		t.Run(name, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			task := policyTask("a", 3)
			task.FallbackPayloads = []json.RawMessage{json.RawMessage(raw)}
			_, err = db.SubmitPlan(context.Background(), PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/bin/host"}, Tasks: []TaskSpec{task}})
			if !errors.Is(err, CodeError("execution_policy_retry_stage_unsupported")) {
				t.Errorf("unsupported fallback admission: %v", err)
			}
			var rows int
			if err = db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM runs)+(SELECT COUNT(*) FROM tasks)+(SELECT COUNT(*) FROM host_launches)`).Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("partial admission=%d %v", rows, err)
			}
		})
	}
}
