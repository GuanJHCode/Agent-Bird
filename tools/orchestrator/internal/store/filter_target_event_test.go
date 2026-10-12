package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Store treats the authenticated Host artifact as opaque. These synthetic
// Host events exercise grant/accept/settlement, not original process proof.
func TestFilterTargetGrantAndNilExitSettlement(t *testing.T) {
	for _, outcome := range []string{"result", "stopped"} {
		t.Run(outcome, func(t *testing.T) { filterTargetGrantSettlement(t, outcome) })
	}
}
func filterTargetGrantSettlement(t *testing.T, outcome string, targetReview ...bool) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var producer adapter.InvocationPayload
	if err = json.Unmarshal(gitFilterPayload(t), &producer); err != nil {
		t.Fatal(err)
	}
	producer.Provider = string(adapter.ProviderClaude)
	producer.Directory = "/private/provider"
	producer.ProviderLock = &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: adapter.BinaryPin{Path: "/private/claude", Version: "fixture", SHA256: strings.Repeat("a", 64)}}
	validator := adapter.InvocationPayload{Kind: "candidate", Directory: "/private/validator-work", GitFilter: producer.GitFilter, CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "validate", SourceTask: "edit", DependencyInputVersion: 1, AutoDirectory: true, Command: []string{"/private/validator"}, CommandSHA256: strings.Repeat("a", 64)}}
	reviewer := adapter.InvocationPayload{Directory: "/private/review", Provider: producer.Provider, ProviderLock: producer.ProviderLock, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000}, GitFilter: producer.GitFilter, CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "review", SourceTask: "validate", DependencyInputVersion: 1, AutoDirectory: true}}
	observer := adapter.InvocationPayload{Kind: "candidate", Directory: "/private/observe", GitFilter: producer.GitFilter, CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "filter_target_preflight", SourceTask: "review", DependencyInputVersion: 1, AutoDirectory: true, Target: adapter.CandidateTarget{Worktree: "/private/repo", Ref: "refs/heads/main", BaseOID: producer.CandidateWorkspace.BaseOID}}}
	ids := []string{"edit", "validate", "review", "observe"}
	payloads := []adapter.InvocationPayload{producer, validator, reviewer, observer}
	if len(targetReview) >= 1 && targetReview[0] {
		approval := reviewer
		approval.Directory = "/private/target-review"
		approval.CandidateAction = &adapter.CandidateAction{Version: 1, Operation: "filter_target_review", SourceTask: "observe", DependencyInputVersion: 1, AutoDirectory: true, Target: observer.CandidateAction.Target}
		ids = append(ids, "target-review")
		payloads = append(payloads, approval)
	}

	if len(targetReview) == 2 && targetReview[1] {
		integrate := observer
		integrate.Directory = "/private/integrate"
		integrate.CandidateAction = &adapter.CandidateAction{Version: 1, Operation: "filter_integrate", SourceTask: "target-review", DependencyInputVersion: 1, AutoDirectory: true, Target: observer.CandidateAction.Target}
		ids = append(ids, "integrate")
		payloads = append(payloads, integrate)
	}
	tasks := make([]TaskSpec, len(ids))
	for i, p := range payloads {
		raw, e := json.Marshal(p)
		if e != nil {
			t.Fatal(e)
		}
		tasks[i] = TaskSpec{ID: ids[i], RunID: "run", MaxAttempts: 1, MaxActiveMS: 1000, AdapterPayload: raw}
		if i > 0 {
			tasks[i].Dependencies = []string{ids[i-1]}
		}
	}
	if len(ids) == 6 {
		tasks[5].Dependencies = []string{"observe", "target-review"}
	}
	plan, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/host"}, Tasks: tasks})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "gen", PID: 10, Birth: "host", Executable: "/private/host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	sequence := int64(0)
	zero := 0
	for i, id := range ids {
		g, e := db.ClaimReady(ctx, host, 1, 2)
		if e != nil || g.TaskID != id {
			t.Fatalf("claim %s grant=%+v err=%v", id, g, e)
		}
		if i == 3 {
			if g.Input == nil || g.Input.Event.TaskID != "review" || g.DependencyInputs == nil || len(g.DependencyInputs.Inputs) != 1 || g.ValidationTool != nil || contract.ValidateDependencyInputs(g) != nil || contract.RequireGitFilter(g) != nil {
				t.Fatalf("unbound preflight grant=%+v", g)
			}
			pending, e := db.PendingLaunches(ctx, host, 1)
			if e != nil || len(pending) != 1 || pending[0].LaunchIntentHash != g.LaunchIntentHash || pending[0].Input.Event.EventID != g.Input.Event.EventID {
				t.Fatalf("preflight replay=%v err=%v", pending, e)
			}
		}
		if i == 4 && (g.Input == nil || g.Input.Event.TaskID != "observe" || g.DependencyInputs == nil || len(g.DependencyInputs.Inputs) != 1 || g.ValidationTool != nil || contract.ValidateDependencyInputs(g) != nil || contract.RequireGitFilter(g) != nil) {
			t.Fatalf("unbound target-review grant=%+v", g)
		}

		if i == 5 {
			if g.Input == nil || g.Input.Event.TaskID != "target-review" || g.DependencyInputs == nil || len(g.DependencyInputs.Inputs) != 2 || g.GitFilterCarrier == nil {
				t.Fatalf("unbound integrate grant=%+v", g)
			}
			pending, e := db.PendingLaunches(ctx, host, 1)
			if e != nil || len(pending) != 1 || pending[0].LaunchIntentHash != g.LaunchIntentHash || *pending[0].GitFilterCarrier != *g.GitFilterCarrier {
				t.Fatalf("integration replay err=%v", e)
			}
		}
		body := []byte("opaque authenticated " + id)
		sequence++
		result := contract.Event{Version: 1, ProducerID: host, EventID: id + "-result", RunID: g.RunID, TaskID: id, AttemptID: g.AttemptID, SegmentID: g.SegmentID, CommandID: g.CommandID, WorkRevision: g.WorkRevision, ExecutionEpoch: g.ExecutionEpoch, Sequence: sequence, Kind: contract.EventResult, PayloadHash: runtimeHash(string(body)), Artifact: managedHostArtifactForTest(t, db, g, "terminal", 0, body)}
		if i == 3 {
			for _, mode := range []string{"fake_exit", "spawned", "session"} {
				bad := result
				bad.EventID = "bad-" + mode
				switch mode {
				case "fake_exit":
					bad.ExitCode = &zero
				case "spawned":
					bad.Kind = contract.EventSpawned
				case "session":
					bad.SessionID = "fake"
				}
				before := diagnosticsBusinessSnapshot(t, db)
				if _, e = db.CommitHostEvent(ctx, bad); e == nil {
					t.Fatalf("accepted %s", mode)
				}
				if diagnosticsBusinessSnapshot(t, db) != before {
					t.Fatalf("%s changed durable state", mode)
				}
			}
		}
		stopped := i == 3 && outcome == "stopped"
		if stopped {
			result.Kind, result.StopReason = contract.EventStopped, "owner_stop_requested"
		}
		if _, e = db.CommitHostEvent(ctx, result); e != nil {
			t.Fatalf("result %s: %v", id, e)
		}
		sequence++
		done := result
		done.Sequence = sequence
		done.EventID = id + "-exited"
		done.Kind = contract.EventExited
		done.StopReason = ""
		done.ActiveMS = 1
		if i != 3 && i != 5 {
			done.ExitCode = &zero
		}
		f := g.GitFilter
		done.GitFilterCleanup = &contract.GitFilterCleanup{Version: 1, ReservationID: f.ReservationID, ScopeSHA256: f.ScopeSHA256, ManifestSHA256: f.ManifestSHA256, CleanToolSHA256: f.CleanToolSHA256, SmudgeToolSHA256: f.SmudgeToolSHA256, JournalSHA256: runtimeHash("synthetic cleanup"), RemovedBytes: 0}

		if c := g.GitFilterCarrier; c != nil {
			done.GitFilterCarrierCleanup = &contract.GitFilterCarrierCleanup{Version: 1, ReservationID: c.ReservationID, ScopeSHA256: c.ScopeSHA256, FilterReservationID: c.FilterReservationID, ManifestSHA256: c.ManifestSHA256, RuntimeNamespace: c.RuntimeNamespace, Outcome: "not_created", JournalSHA256: runtimeHash("")}
			bad := done
			bad.GitFilterCarrierCleanup = nil
			before := diagnosticsBusinessSnapshot(t, db)
			if _, e = db.CommitHostEvent(ctx, bad); e == nil || diagnosticsBusinessSnapshot(t, db) != before {
				t.Fatalf("missing carrier receipt changed state: %v", e)
			}
		}
		if v := g.ValidationTool; v != nil {
			done.ValidationToolCleanup = &contract.ValidationToolCleanup{Version: 1, ReservationID: v.ReservationID, ScopeSHA256: v.ScopeSHA256, ToolSHA256: v.ToolSHA256, JournalSHA256: runtimeHash("synthetic validation cleanup"), RemovedBytes: 0}
		}
		if i == 3 {
			bad := done
			bad.GitFilterCleanup = nil
			before := diagnosticsBusinessSnapshot(t, db)
			if _, e = db.CommitHostEvent(ctx, bad); e == nil || diagnosticsBusinessSnapshot(t, db) != before {
				t.Fatalf("unproved exit changed state: %v", e)
			}
		}
		missingProviderExit := i == 4 && outcome == "nil-provider-exit"
		if missingProviderExit {
			done.ExitCode = nil
		}
		if _, e = db.CommitHostEvent(ctx, done); e != nil {
			t.Fatalf("exited %s: %v", id, e)
		}
		var status, state string
		expectedStatus := "result_ready"
		if missingProviderExit {
			expectedStatus = "failed"
		}
		if stopped {
			expectedStatus = "interrupted"
		}
		if e = db.sql.QueryRow(`SELECT status FROM tasks WHERE id=?`, id).Scan(&status); e != nil || status != expectedStatus {
			t.Fatalf("%s status=%s err=%v", id, status, e)
		}
		if e = db.sql.QueryRow(`SELECT state FROM git_filter_resource_reservations WHERE id=?`, f.ReservationID).Scan(&state); e != nil || state != "released" {
			t.Fatalf("%s resource=%s err=%v", id, state, e)
		}
		if stopped {
			return
		}
		page, e := db.CollectPending(ctx, id, "", 0, false)
		if e != nil || len(page.Events) != 1 {
			t.Fatalf("pending %s: %v", id, e)
		}
		accepted := page.Events[0]
		if missingProviderExit {
			before := diagnosticsBusinessSnapshot(t, db)
			_, e = db.ReviewResult(ctx, ReviewSpec{TaskID: id, WorkRevision: 1, EventID: accepted.EventID, EventRevision: accepted.EventRevision, EventHash: accepted.PayloadHash, ActionSlot: accepted.ActionSlot, Decision: "accept", CommandID: "reject-unproved-approval"})
			if e == nil || diagnosticsBusinessSnapshot(t, db) != before {
				t.Fatalf("nil-provider-exit result gained approval: %v", e)
			}
			return
		}
		if _, e = db.ReviewResult(ctx, ReviewSpec{TaskID: id, WorkRevision: 1, EventID: accepted.EventID, EventRevision: accepted.EventRevision, EventHash: accepted.PayloadHash, ActionSlot: accepted.ActionSlot, Decision: "accept", CommandID: "accept-" + id}); e != nil {
			t.Fatalf("accept %s: %v", id, e)
		}
	}
}

func TestFilterTargetReviewGrantAndProviderExit(t *testing.T) {
	for _, outcome := range []string{"result", "nil-provider-exit"} {
		t.Run(outcome, func(t *testing.T) { filterTargetGrantSettlement(t, outcome, true) })
	}
}

func TestFilterIntegrationPublicGrantAndSettlement(t *testing.T) {
	filterTargetGrantSettlement(t, "result", true, true)
}
