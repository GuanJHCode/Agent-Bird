package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
)

func budgetPolicyPlan(run, owner string, policy *contract.BudgetPolicy, ids ...string) PlanSpec {
	p := PlanSpec{Run: RunSpec{ID: run, ControllerThread: owner, PlanRevision: 1, OriginContextID: run, OriginPID: 1, OriginBirth: "owner"}, Host: HostLaunchSpec{OriginContextID: run, HostGeneration: run, Executable: "/private/bin/host"}}
	for _, id := range ids {
		p.Tasks = append(p.Tasks, TaskSpec{ID: id, RunID: run, MaxAttempts: 3, MaxActiveMS: 120000, BudgetGroupID: "shared", BudgetPolicy: policy})
	}
	return p
}

func budgetPolicyHost(t *testing.T, db *DB, p PlanSpec, epoch uint64) string {
	t.Helper()
	ctx := context.Background()
	receipt, err := db.SubmitPlan(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: p.Run.ID, OriginPID: 1, OriginBirth: "owner", HostGeneration: p.Run.ID, PID: 2, Birth: "host", Executable: "/private/bin/host"}, epoch)
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func budgetPolicyExit(t *testing.T, db *DB, g contract.LaunchCommand, used int64) {
	t.Helper()
	for i, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventStopped, contract.EventExited} {
		e := contract.Event{Version: 1, ProducerID: g.HostID, EventID: fmt.Sprintf("%s-%d", g.SegmentID, i), Sequence: int64(i + 1), RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, CommandID: g.CommandID, WorkRevision: g.WorkRevision, ExecutionEpoch: g.ExecutionEpoch, Kind: kind, PayloadHash: fmt.Sprintf("hash-%s-%d", g.SegmentID, i)}
		if kind == contract.EventExited {
			e.ActiveMS = used
		}
		if _, err := db.CommitHostEvent(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBudgetPolicyTotalGrantsOnlyRemainingAfterMeasuredSettlement(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	policy := &contract.BudgetPolicy{Version: 2, MaxActiveMSPerSegment: 120000, MaxActiveMSTotal: 180000}
	host := budgetPolicyHost(t, db, budgetPolicyPlan("run", "owner", policy, "a", "b"), 1)
	first, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil || first.GrantedActiveMS != 120000 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	budgetPolicyExit(t, db, first, 110000)
	second, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil || second.GrantedActiveMS != 70000 {
		t.Fatalf("second grant=%d, want70000; %v", second.GrantedActiveMS, err)
	}
}

func TestBudgetPolicyReservationsSurviveRestartAndSharedTaskIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	policy := &contract.BudgetPolicy{Version: 2, MaxActiveMSPerSegment: 120000, MaxActiveMSTotal: 180000}
	host := budgetPolicyHost(t, db, budgetPolicyPlan("run", "owner", policy, "a", "b", "c"), 1)
	a, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	// No exited event means all granted time stays reserved, including unknown.
	if _, err = db.sql.Exec(`UPDATE segment_runtime SET status='unknown' WHERE segment_id=?`, a.SegmentID); err != nil {
		t.Fatal(err)
	}
	b, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil || b.GrantedActiveMS != 60000 {
		t.Fatalf("reserved second=%d err=%v", b.GrantedActiveMS, err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	budgetPolicyExit(t, db, b, 60000) // free a real slot while retaining the unknown reservation
	if _, err = db.ClaimReady(ctx, host, 1, 2); err == nil || err.Error() != "budget_exhausted" {
		t.Fatalf("restart replenished total: %v", err)
	}
	var charged int64
	if err = db.sql.QueryRow(`SELECT SUM(granted_ms) FROM budget_runtime WHERE budget_group_id='shared'`).Scan(&charged); err != nil || charged != 180000 {
		t.Fatalf("charged=%d %v", charged, err)
	}
}

func TestBudgetPolicyIsImmutableAcrossRunsAndOwners(t *testing.T) {
	for _, kind := range []string{"time", "attempts", "owner", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			policy := &contract.BudgetPolicy{Version: 2, MaxActiveMSPerSegment: 120000, MaxActiveMSTotal: 180000}
			_ = budgetPolicyHost(t, db, budgetPolicyPlan("run", "owner", policy, "a"), 1)
			next := *policy
			p := budgetPolicyPlan("other", "owner", &next, "b")
			switch kind {
			case "time":
				next.MaxActiveMSTotal++
			case "attempts":
				p.Tasks[0].MaxAttempts = 2
			case "owner":
				p.Run.ControllerThread = "another-owner"
			case "legacy":
				p.Tasks[0].BudgetPolicy = nil
			}
			if _, err = db.SubmitPlan(ctx, p); err == nil {
				t.Fatalf("%s replaced original budget group", kind)
			}
			var runs int
			if err = db.sql.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&runs); err != nil || runs != 1 {
				t.Fatalf("rejection created run: %d %v", runs, err)
			}
		})
	}
}

func TestBudgetPolicySameGroupAcrossRunsKeepsTotal(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	policy := &contract.BudgetPolicy{Version: 2, MaxActiveMSPerSegment: 120000, MaxActiveMSTotal: 180000}
	firstHost := budgetPolicyHost(t, db, budgetPolicyPlan("run", "owner", policy, "a"), 1)
	first, err := db.ClaimReady(ctx, firstHost, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	budgetPolicyExit(t, db, first, 110000)
	nextHost := budgetPolicyHost(t, db, budgetPolicyPlan("other", "owner", policy, "other-task"), 1)
	second, err := db.ClaimReady(ctx, nextHost, 1, 2)
	if err != nil || second.GrantedActiveMS != 70000 {
		t.Fatalf("new run/task replenished grant: %d %v", second.GrantedActiveMS, err)
	}
}

func TestBudgetPolicyAdmissionBoundsAndLegacySemantics(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, 4, 64} {
		p := budgetPolicyPlan("run", "owner", nil, "task")
		p.Tasks[0].MaxAttempts = n
		if err := validatePlan(p); (n >= 1 && n <= 3) != (err == nil) {
			t.Fatalf("attempts%d=%v", n, err)
		}
	}
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	host := budgetPolicyHost(t, db, budgetPolicyPlan("run", "owner", nil, "a", "b"), 1)
	first, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	budgetPolicyExit(t, db, first, 110000)
	second, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil || second.GrantedActiveMS != 120000 {
		t.Fatalf("legacy per-segment allowance changed: %d %v", second.GrantedActiveMS, err)
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	b, err := summarizeBudget(ctx, tx, "b")
	if err != nil || b.Policy != "legacy_policy" || b.MaxActiveMSTotal != 7200000 || b.UsedMS != 110000 || b.ReservedMS != 120000 {
		t.Fatalf("legacy projection %+v %v", b, err)
	}
}

func TestBudgetPolicyProjectionAndFailedAdmissionDoNotAllocate(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	policy := &contract.BudgetPolicy{Version: 2, MaxActiveMSPerSegment: 120000, MaxActiveMSTotal: 180000}
	host := budgetPolicyHost(t, db, budgetPolicyPlan("run", "owner", policy, "a", "b"), 1)
	g, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := summarizeBudget(ctx, tx, g.TaskID)
	tx.Rollback()
	if err != nil || b.Policy != "group_policy_v2" || b.UsedMS != 0 || b.ReservedMS != 120000 || b.RemainingMS != 60000 || b.AttemptsUsed != 1 || b.AttemptsRemaining != 2 {
		t.Fatalf("projection %+v %v", b, err)
	}
	invalid := *policy
	invalid.MaxActiveMSTotal = 0
	if _, err = db.SubmitPlan(ctx, budgetPolicyPlan("bad", "owner", &invalid, "bad-task")); err == nil {
		t.Fatal("invalid budget admitted")
	}
	var runs, attempts, groups int
	db.sql.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&runs)
	db.sql.QueryRow(`SELECT COUNT(*) FROM attempts`).Scan(&attempts)
	db.sql.QueryRow(`SELECT COUNT(*) FROM group_budget_policies`).Scan(&groups)
	if runs != 1 || attempts != 1 || groups != 1 {
		t.Fatalf("failed request allocated: runs=%d attempts=%d groups=%d", runs, attempts, groups)
	}
}

func TestBudgetPolicyReworkConsumesOriginalGroupAndKeepsCandidateReviewable(t *testing.T) {
	p := &contract.BudgetPolicy{Version: 2, MaxActiveMSPerSegment: 1000, MaxActiveMSTotal: 150}
	f := newOwnerReworkArtifactBudgetFixture(t, nil, p)
	ctx := context.Background()
	if _, err := f.db.QueueRequestChanges(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	next, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil || next.GrantedActiveMS != 50 || next.BudgetGroupID != f.grant.BudgetGroupID {
		t.Fatalf("rework budget %+v %v", next, err)
	}
	spec := commitOwnerNextCandidate(t, f, next)
	if _, err = f.db.QueueRequestChanges(ctx, spec); err == nil || err.Error() != "budget_exhausted" {
		t.Fatalf("exhausted group reworked: %v", err)
	}
	summary, err := f.db.SummarizeRun(ctx, "edit", "owner", f.token)
	if err != nil {
		t.Fatal(err)
	}
	task := summary.Tasks[0]
	if task.Budget == nil || task.Budget.UsedMS != 150 || task.Budget.RemainingMS != 0 || task.ReviewableResult == nil || task.ReviewableResult.CandidateOID == "" || task.RequestChangesBlockedReason != "budget_exhausted" {
		t.Fatalf("projection %+v", task)
	}
}

func TestBudgetPolicyRetryAndExplicitHostRebindDoNotTopUp(t *testing.T) {
	p := &contract.BudgetPolicy{Version: 2, MaxActiveMSPerSegment: 1000, MaxActiveMSTotal: 1500}
	f := newReviewRetryBudgetFixture(t, contract.EventStopped, p)
	ctx := context.Background()
	if err := f.db.BeginCoordinatorEpoch(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if err := f.db.AuthorizeHostRebind(ctx, f.hello.LaunchID, f.hello.PID, f.hello.Birth); err != nil {
		t.Fatal(err)
	}
	hello := f.hello
	hello.PID++
	hello.Birth = "replacement"
	host, err := f.db.RegisterHost(ctx, hello, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.QueueRetry(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	next, err := f.db.ClaimReady(ctx, host, 2, 2)
	if err != nil || next.GrantedActiveMS != 500 || next.BudgetGroupID != "review" {
		t.Fatalf("retry/rebind replenished %+v %v", next, err)
	}
}

func TestBudgetPolicyAttemptsAreSharedAcrossNewRuns(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	policy := &contract.BudgetPolicy{Version: 2, MaxActiveMSPerSegment: 120000, MaxActiveMSTotal: 180000}
	for i := 0; i < 4; i++ {
		name := fmt.Sprintf("run%d", i)
		host := budgetPolicyHost(t, db, budgetPolicyPlan(name, "owner", policy, name+"-task"), 1)
		g, err := db.ClaimReady(context.Background(), host, 1, 2)
		if i == 3 {
			if err == nil || err.Error() != "attempt_limit" {
				t.Fatalf("split runs replenished attempts: %v", err)
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		budgetPolicyExit(t, db, g, 1)
	}
}

func TestBudgetPolicyRejectsSubmitLaunchIdentifierGapBeforeAllocation(t *testing.T) {
	// Independent review found this request fits Submit (65455 bytes) but not
	// Launch. Admission must reject it, not create an unlaunchable queued task.
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := budgetPolicyPlan("run", "owner", &contract.BudgetPolicy{Version: 2, MaxActiveMSPerSegment: 1000, MaxActiveMSTotal: 1000}, "task")
	p.Tasks[0].MaxActiveMS = 1000
	p.Tasks[0].BudgetGroupID = strings.Repeat("g", 64800)
	if _, err = db.SubmitPlan(context.Background(), p); err == nil {
		t.Fatal("oversized group admitted")
	}
	for _, table := range []string{"runs", "tasks", "attempts", "budget_runtime", "group_budget_policies"} {
		var count int
		if err = db.sql.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s allocated %d %v", table, count, err)
		}
	}
	for _, field := range []string{"run", "task", "group", "launch"} {
		for _, id := range []string{strings.Repeat("x", 129), strings.Repeat("中", 43)} {
			p = budgetPolicyPlan("run", "owner", nil, "task")
			switch field {
			case "run":
				p.Run.ID = id
				p.Tasks[0].RunID = id
			case "task":
				p.Tasks[0].ID = id
			case "group":
				p.Tasks[0].BudgetGroupID = id
			case "launch":
				p.LaunchID = id
			}
			if _, err = db.SubmitPlan(context.Background(), p); err == nil {
				t.Fatalf("oversized %s admitted", field)
			}
		}
	}
}

func TestAcceptedInputWithMaximumControlIDsAndAdapterFitsLaunchFrame(t *testing.T) {
	// Two separately encoded 16KiB adapters and a source event repeat these
	// identifiers. Check the actual launch envelope, not a character estimate.
	id := strings.Repeat("<", contract.MaxControlIDBytes)
	raw, _ := json.Marshal(map[string]string{"kind": "fake", "prompt": strings.Repeat("<", 2600)})
	if err := contract.ValidateAdapterSize(raw); err != nil {
		t.Fatal(err)
	}
	c := contract.LaunchCommand{CommandID: id, HostID: id, RunID: id, TaskID: id, AttemptID: id, SegmentID: id, BudgetGroupID: id, ReservationID: id, SlotToken: id, ReportCapabilityID: id, ExecutionEpoch: ^uint64(0), AdapterPayload: raw, Input: &contract.AcceptedInput{AdapterPayload: raw, DecisionCommandID: id, Event: contract.Event{Version: 1, ProducerID: id, EventID: id, ActionSlot: id, RunID: id, TaskID: id, AttemptID: id, SegmentID: id, CommandID: id, Kind: contract.EventResult, Artifact: &contract.ArtifactRef{ID: id, Path: "/" + strings.Repeat("<", 1023), SHA256: strings.Repeat("a", 64), Size: 1048576}, PayloadHash: strings.Repeat("b", 64)}}}
	if err := validateLaunchSize(c); err != nil {
		t.Fatalf("bounded accepted input does not fit: %v", err)
	}
}
