package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/gitops"
	"codex-cli-orchestration-design/tools/orchestrator/internal/gitopsworker"
)

type ownerReworkFixture struct {
	db    *DB
	spec  OwnerReworkSpec
	host  string
	hello contract.HostHello
	grant contract.LaunchCommand
	event contract.Event
	path  string
	token string
}

func newOwnerReworkFixture(t *testing.T, mutations ...func(*gitops.CandidateReceipt)) ownerReworkFixture {
	t.Helper()
	return newOwnerReworkArtifactFixture(t, nil, mutations...)
}
func newOwnerReworkArtifactFixture(t *testing.T, artifactMutation func(map[string]any), mutations ...func(*gitops.CandidateReceipt)) ownerReworkFixture {
	return newOwnerReworkArtifactBudgetFixture(t, artifactMutation, nil, mutations...)
}

func newOwnerReworkArtifactBudgetFixture(t *testing.T, artifactMutation func(map[string]any), policy *contract.BudgetPolicy, mutations ...func(*gitops.CandidateReceipt)) ownerReworkFixture {
	t.Helper()
	return newOwnerReworkArtifactRevisionFixture(t, artifactMutation, policy, 1, mutations...)
}

func newOwnerReworkArtifactRevisionFixture(t *testing.T, artifactMutation func(map[string]any), policy *contract.BudgetPolicy, revision int, mutations ...func(*gitops.CandidateReceipt)) ownerReworkFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repoRoot := filepath.Join(root, "repo")
	work := filepath.Join(root, "work")
	if err = os.Mkdir(repoRoot, 0700); err != nil {
		t.Fatal(err)
	}
	ownerFixtureGit(t, repoRoot, "init", "-q")
	ownerFixtureGit(t, repoRoot, "config", "user.name", "Test")
	ownerFixtureGit(t, repoRoot, "config", "user.email", "test@example.invalid")
	if err = os.WriteFile(filepath.Join(repoRoot, "calc.py"), []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ownerFixtureGit(t, repoRoot, "add", "calc.py")
	ownerFixtureGit(t, repoRoot, "commit", "-qm", "base")
	base := ownerFixtureGit(t, repoRoot, "rev-parse", "HEAD")
	lock := adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: adapter.BinaryPin{Path: "/private/bin/claude", Version: "2.1.267 (Claude Code)", SHA256: strings.Repeat("a", 64)}}
	payload, _ := json.Marshal(adapter.InvocationPayload{Provider: string(adapter.ProviderClaude), Directory: work, Prompt: "original", ProviderLock: &lock, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: 1000}, CandidateWorkspace: &adapter.CandidateWorkspace{Version: 1, RepoRoot: repoRoot, BaseOID: base, Paths: []string{"calc.py"}}})
	plan, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 7, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"}, Tasks: []TaskSpec{{ID: "edit", RunID: "run", WorkRevision: revision, MaxAttempts: 3, MaxActiveMS: 1000, BudgetPolicy: policy, AdapterPayload: payload}}})
	if err != nil {
		t.Fatal(err)
	}
	hello := contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "gen", PID: 10, Birth: "host", Executable: "/private/bin/orchestrator", OwnerRequestChangesV2: true}
	host, err := db.RegisterHost(ctx, hello, 1)
	if err != nil {
		t.Fatal(err)
	}
	g, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	c := freezeOwnerFixtureCandidate(t, root, g)

	for _, mutate := range mutations {
		mutate(&c)
	}
	body, err := gitops.CandidateArtifact("completed", c)
	if err != nil {
		t.Fatal(err)
	}
	if artifactMutation != nil {
		var value map[string]any
		if err = json.Unmarshal(body, &value); err != nil {
			t.Fatal(err)
		}
		artifactMutation(value)
		body, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "candidate.json")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	e := contract.Event{Version: 1, ProducerID: host, EventID: "candidate", RunID: "run", TaskID: "edit", AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, ExecutionEpoch: 1, CommandID: g.CommandID, Sequence: 1, Kind: contract.EventResult, PayloadHash: runtimeHash(string(body)), Artifact: &contract.ArtifactRef{ID: "candidate", Path: path, Size: int64(len(body)), SHA256: runtimeHash(string(body))}}
	if _, err = db.CommitHostEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	done := e
	done.EventID = "exited"
	done.Sequence = 2
	done.Kind = contract.EventExited
	done.ActiveMS = 100
	zero := 0
	done.ExitCode = &zero
	if _, err = db.CommitHostEvent(ctx, done); err != nil {
		t.Fatal(err)
	}
	page, err := db.CollectPending(ctx, "edit", "", 0, false)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("collect %+v %v", page, err)
	}
	e = page.Events[0]
	spec := OwnerReworkSpec{TaskID: "edit", WorkRevision: g.WorkRevision, EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, SegmentID: e.SegmentID, CandidateOID: c.CandidateOID, Feedback: "Handle negatives", Acceptance: "add(-2,3)==1", CommandID: "owner-change-1"}
	return ownerReworkFixture{db, spec, host, hello, g, e, path, plan.ControlToken}
}

func TestOwnerRequestChangesRevisesWithoutReviewerAndPreservesEvidence(t *testing.T) {
	for _, ack := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-ack", true: "after-ack"}[ack], func(t *testing.T) {
			f := newOwnerReworkFixture(t)
			ctx := context.Background()
			before, _ := os.ReadFile(f.path)
			if ack {
				_, err := f.db.CollectPending(ctx, "edit", "", 0, false)
				if err != nil {
					t.Fatal(err)
				}
				if err = f.db.AckDecisions(ctx, "edit", "ack-delivery", runtimeHash("proof"), []AckDecision{{EventID: f.event.EventID, EventRevision: f.event.EventRevision, EventHash: f.event.PayloadHash, ActionSlot: f.event.ActionSlot, Decision: "waiting_user", CommandID: "ack"}}); err != nil {
					t.Fatal(err)
				}
			}
			r, err := f.db.QueueRequestChanges(ctx, f.spec)
			if err != nil {
				t.Fatal(err)
			}
			if r.WorkRevision != 2 || r.Status != "rework_queued" {
				t.Fatalf("receipt %+v", r)
			}
			if !ack {
				if err = f.db.AckDecision(ctx, "edit", AckDecision{EventID: f.event.EventID, EventRevision: f.event.EventRevision, EventHash: f.event.PayloadHash, ActionSlot: f.event.ActionSlot, Decision: "handled", CommandID: "ack-after-changes"}); err != nil {
					t.Fatalf("business-bound ACK after revision: %v", err)
				}
			}
			again, err := f.db.QueueRequestChanges(ctx, f.spec)
			if err != nil || !reflect.DeepEqual(r, again) {
				t.Fatalf("replay %+v %v", again, err)
			}
			changed := f.spec
			changed.Feedback = "different"
			if _, err = f.db.QueueRequestChanges(ctx, changed); err == nil {
				t.Fatal("changed replay accepted")
			}
			changed = f.spec
			changed.CommandID = "another"
			if _, err = f.db.QueueRequestChanges(ctx, changed); err == nil {
				t.Fatal("different command replay accepted")
			}
			if _, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "edit", WorkRevision: 1, EventID: f.spec.EventID, EventRevision: f.spec.EventRevision, EventHash: f.spec.EventHash, ActionSlot: f.spec.ActionSlot, Decision: "accept", CommandID: "old-accept"}); err == nil {
				t.Fatal("old accept accepted")
			}
			if _, err = f.db.QueueRework(ctx, ReworkSpec{TaskID: "edit", WorkRevision: 1, CandidateOID: f.spec.CandidateOID}); err == nil {
				t.Fatal("old rework accepted")
			}
			g, err := f.db.ClaimReady(ctx, f.host, 1, 2)
			if err != nil {
				t.Fatal(err)
			}
			p, err := adapter.DecodeInvocationPayload(g.AdapterPayload)
			if err != nil {
				t.Fatal(err)
			}
			if g.WorkRevision != 2 || p.CandidateWorkspace.BaseOID != f.spec.CandidateOID || p.Directory == filepath.Join(filepath.Dir(f.path), "work") || p.CandidateWorkspace.Rework.Version != 2 || p.CandidateWorkspace.Rework.Source != "owner" || p.CandidateWorkspace.Rework.OwnerDecisionID != f.spec.CommandID {
				t.Fatalf("unsafe next revision %+v %+v", g, p)
			}
			after, _ := os.ReadFile(f.path)
			if string(before) != string(after) {
				t.Fatal("old artifact changed")
			}
		})
	}
}

func TestOwnerRequestChangesEligibilityAndSummaryAgree(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		want      string
	}{
		{"single-attempt", `UPDATE tasks SET max_attempts=1`, "attempt_limit"},
		{"host-offline", `UPDATE runtime_hosts SET status='offline'`, "rework_host_not_ready"},
		{"old-host", `UPDATE runtime_hosts SET owner_request_changes_v2=0`, "host_upgrade_required"},
		{"unknown", `UPDATE segment_runtime SET status='unknown'`, "rework_active_segment"},
		{"budget", `UPDATE budget_runtime SET used_ms=7200000`, "budget_exhausted"},
		{"provider", `INSERT INTO provider_settings VALUES('owner','claude-code',0,NULL)`, "provider_disabled"},
		{"dependency", `UPDATE tasks SET dependencies_json='["edit"]'`, "owner_rework_unsupported"},
		{"explicit-session", `UPDATE task_runtime SET adapter_payload=json_set(adapter_payload,'$.session_id','resume-me')`, "owner_rework_unsupported"},
		{"fallback", `UPDATE task_runtime SET fallback_payloads='[{}]'`, "owner_rework_unsupported"},
		{"candidate-action", `UPDATE task_runtime SET adapter_payload=json_set(adapter_payload,'$.candidate_action',json('{"version":1,"operation":"review"}'))`, "owner_rework_unsupported"},
		{"successor", `INSERT INTO tasks SELECT 'peer',run_id,'queued','["edit"]',3,created_at FROM tasks WHERE id='edit'`, "owner_rework_unsupported"},
		{"shared-budget", `INSERT INTO tasks SELECT 'peer',run_id,'completed','[]',3,created_at FROM tasks WHERE id='edit'; INSERT INTO task_runtime(task_id,host_launch_id,work_revision,budget_group_id,max_active_ms) SELECT 'peer',host_launch_id,1,budget_group_id,max_active_ms FROM task_runtime WHERE task_id='edit'`, "owner_rework_unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOwnerReworkFixture(t)
			ctx := context.Background()
			if _, err := f.db.sql.Exec(tc.sql); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.QueueRequestChanges(ctx, f.spec); err == nil || err.Error() != tc.want {
				t.Fatalf("got %v want %s", err, tc.want)
			}
			summary, err := f.db.SummarizeRun(ctx, "edit", "owner", f.token)
			if err != nil || len(summary.Tasks) < 1 || slices.Contains(summary.Tasks[0].AllowedActions, "request-changes") || summary.Tasks[0].RequestChangesBlockedReason != tc.want {
				t.Fatalf("summary differs from mutation: %+v %v", summary, err)
			}

		})
	}
}

func TestOwnerRequestChangesAndAcceptAreMutuallyExclusive(t *testing.T) {
	for i := 0; i < 8; i++ {
		f := newOwnerReworkFixture(t)
		start := make(chan struct{})
		var wg sync.WaitGroup
		var a, b error
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, a = f.db.QueueRequestChanges(context.Background(), f.spec) }()
		go func() {
			defer wg.Done()
			<-start
			_, b = f.db.ReviewResult(context.Background(), ReviewSpec{TaskID: "edit", WorkRevision: 1, EventID: f.spec.EventID, EventRevision: f.spec.EventRevision, EventHash: f.spec.EventHash, ActionSlot: f.spec.ActionSlot, Decision: "accept", CommandID: "accept"})
		}()
		close(start)
		wg.Wait()
		if (a == nil) == (b == nil) {
			t.Fatalf("expected exactly one decision: request=%v accept=%v", a, b)
		}
	}
}

func TestOwnerRequestChangesHostDowngradeBlocksLaunch(t *testing.T) {
	f := newOwnerReworkFixture(t)
	if _, err := f.db.QueueRequestChanges(context.Background(), f.spec); err != nil {
		t.Fatal(err)
	}
	hello := f.hello
	hello.OwnerRequestChangesV2 = false
	if _, err := f.db.RegisterHost(context.Background(), hello, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ClaimReady(context.Background(), f.host, 1, 2); err == nil {
		t.Fatal("old Host launched v2")
	}
}

func TestOwnerRequestChangesRetainsOnlyUsefulCapableHost(t *testing.T) {
	f := newOwnerReworkFixture(t)
	ctx := context.Background()
	hosts, err := f.db.RetirableHosts(ctx)
	if err != nil || slices.Contains(hosts, f.host) {
		t.Fatalf("useful Host prematurely retirable: %v %v", hosts, err)
	}
	if err = f.db.RetireHost(ctx, f.host); err == nil {
		t.Fatal("retirement bypassed owner eligibility")
	}
	if _, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "edit", WorkRevision: 1, EventID: f.spec.EventID, EventRevision: f.spec.EventRevision, EventHash: f.spec.EventHash, ActionSlot: f.spec.ActionSlot, Decision: "accept", CommandID: "accept"}); err != nil {
		t.Fatal(err)
	}
	if err = f.db.RetireHost(ctx, f.host); err != nil {
		t.Fatalf("accepted task retained Host: %v", err)
	}
}
func TestOwnerRequestChangesRechecksStorageQuestionsAndRestart(t *testing.T) {
	for _, name := range []string{"storage", "quota", "question", "restart", "artifact"} {
		t.Run(name, func(t *testing.T) {
			f := newOwnerReworkFixture(t)
			ctx := context.Background()
			want := ""
			switch name {
			case "storage":
				f.db.minFreeBytes = ^uint64(0)
				want = "storage_blocked"
			case "quota":
				f.db.runControlLimit = 1
				want = "control_spool_full"
			case "question":
				_, err := f.db.sql.Exec(`INSERT INTO runtime_questions(question_id,task_id,attempt_id,segment_id,question_revision,work_revision,session_kind,session_id,status,created_at) VALUES('q','edit',?,?,1,1,'claude','session','open','now')`, f.grant.AttemptID, f.grant.SegmentID)
				if err != nil {
					t.Fatal(err)
				}
				want = "owner_rework_question_pending"
			case "restart":
				if err := f.db.BeginCoordinatorEpoch(ctx, 2); err != nil {
					t.Fatal(err)
				}
				want = "rework_host_not_ready"
			case "artifact":
				if err := os.WriteFile(f.path, []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "rework_artifact_invalid"
			}
			if _, err := f.db.QueueRequestChanges(ctx, f.spec); err == nil || err.Error() != want {
				t.Fatalf("got %v want %s", err, want)
			}
			summary, err := f.db.SummarizeRun(ctx, "edit", "owner", f.token)
			if err != nil || slices.Contains(summary.Tasks[0].AllowedActions, "request-changes") || summary.Tasks[0].RequestChangesBlockedReason != want {
				t.Fatalf("summary diverged %+v %v", summary, err)
			}
		})
	}
}
func TestOwnerRequestChangesNeverResumesObservedNativeSession(t *testing.T) {
	f := newOwnerReworkFixture(t)
	ctx := context.Background()
	if _, err := f.db.sql.Exec(`INSERT INTO segment_sessions VALUES(?,?,'claude','observed-session','observed-event','now')`, f.grant.SegmentID, f.grant.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.QueueRequestChanges(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	grant, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	p, err := adapter.DecodeInvocationPayload(grant.AdapterPayload)
	if err != nil {
		t.Fatal(err)
	}
	if grant.SessionID != "" || p.SessionID != "" || p.SessionKind != "" || p.Directory == filepath.Join(filepath.Dir(f.path), "work") {
		t.Fatalf("observed native session reused %+v %+v", grant, p)
	}
	var count int
	if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM segment_sessions WHERE session_id='observed-session'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("old observation lost count=%d %v", count, err)
	}
}

func TestOwnerRequestChangesRetainsHostAcrossTransientAvailability(t *testing.T) {
	for _, kind := range []string{"provider", "storage", "quota"} {
		t.Run(kind, func(t *testing.T) {
			f := newOwnerReworkFixture(t)
			ctx := context.Background()
			switch kind {
			case "provider":
				if _, err := f.db.sql.Exec(`INSERT INTO provider_settings VALUES('owner','claude-code',0,NULL)`); err != nil {
					t.Fatal(err)
				}
			case "storage":
				f.db.minFreeBytes = ^uint64(0)
			case "quota":
				f.db.runControlLimit = 1
			}
			if _, err := f.db.QueueRequestChanges(ctx, f.spec); err == nil {
				t.Fatal("temporary obstacle was ignored")
			}
			ids, err := f.db.RetirableHosts(ctx)
			if err != nil || slices.Contains(ids, f.host) {
				t.Fatalf("transient outage destroyed recovery: %v %v", ids, err)
			}
			if err = f.db.RetireHost(ctx, f.host); err == nil {
				t.Fatal("retirement bypassed preserved owner decision")
			}
			switch kind {
			case "provider":
				if _, err = f.db.sql.Exec(`UPDATE provider_settings SET enabled=1`); err != nil {
					t.Fatal(err)
				}
			case "storage":
				f.db.minFreeBytes = 0
			case "quota":
				f.db.runControlLimit = 16 * 1024 * 1024
			}
			if _, err = f.db.QueueRequestChanges(ctx, f.spec); err != nil {
				t.Fatalf("restored availability cannot revise: %v", err)
			}
		})
	}
}
func TestOwnerRequestChangesAndRetirementCAS(t *testing.T) {
	for i := 0; i < 8; i++ {
		f := newOwnerReworkFixture(t)
		start := make(chan struct{})
		var wg sync.WaitGroup
		var queued, retired error
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, queued = f.db.QueueRequestChanges(context.Background(), f.spec) }()
		go func() { defer wg.Done(); <-start; retired = f.db.RetireHost(context.Background(), f.host) }()
		close(start)
		wg.Wait()
		if queued != nil || retired == nil {
			t.Fatalf("lost useful Host: request=%v retire=%v", queued, retired)
		}
	}
}

func TestOwnerRequestChangesPendingReplayAlsoRequiresCapableHost(t *testing.T) {
	f := newOwnerReworkFixture(t)
	ctx := context.Background()
	if _, err := f.db.QueueRequestChanges(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	grant, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	hello := f.hello
	hello.OwnerRequestChangesV2 = false
	if _, err = f.db.RegisterHost(ctx, hello, 1); err != nil {
		t.Fatal(err)
	}
	commands, err := f.db.PendingLaunches(ctx, f.host, 1)
	if err != nil || len(commands) != 0 {
		t.Fatalf("old Host received pending v2: %+v %v", commands, err)
	}
	if _, err = f.db.RegisterHost(ctx, f.hello, 1); err != nil {
		t.Fatal(err)
	}
	commands, err = f.db.PendingLaunches(ctx, f.host, 1)
	if err != nil || len(commands) != 1 || commands[0].CommandID != grant.CommandID || commands[0].AttemptID != grant.AttemptID {
		t.Fatalf("restored Host could not replay exact grant: %+v %v", commands, err)
	}
	var attempts int
	if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM attempts WHERE task_id='edit'`).Scan(&attempts); err != nil || attempts != 2 {
		t.Fatalf("capability change duplicated attempts: %d %v", attempts, err)
	}
}

func TestOwnerRequestChangesRevalidatesEveryCandidateBinding(t *testing.T) {
	for name, mutate := range map[string]func(*gitops.CandidateReceipt){
		"run":             func(c *gitops.CandidateReceipt) { c.Binding.RunID = "other" },
		"task":            func(c *gitops.CandidateReceipt) { c.Binding.TaskID = "other" },
		"attempt":         func(c *gitops.CandidateReceipt) { c.Binding.AttemptID = "other" },
		"segment":         func(c *gitops.CandidateReceipt) { c.Binding.SegmentID = "other" },
		"work-revision":   func(c *gitops.CandidateReceipt) { c.Binding.WorkRevision = 2 },
		"plan-revision":   func(c *gitops.CandidateReceipt) { c.Binding.PlanRevision = 8 },
		"receipt-attempt": func(c *gitops.CandidateReceipt) { c.AttemptID = "other" },
		"receipt-plan":    func(c *gitops.CandidateReceipt) { c.PlanRevision = 8 },
		"repo":            func(c *gitops.CandidateReceipt) { c.RepoRoot = "/private/other" },
		"base":            func(c *gitops.CandidateReceipt) { c.BaseOID = strings.Repeat("f", 40) },
		"tree":            func(c *gitops.CandidateReceipt) { c.TreeOID = c.BaseOID },
		"candidate":       func(c *gitops.CandidateReceipt) { c.CandidateOID = strings.Repeat("f", 40) },
		"worktree":        func(c *gitops.CandidateReceipt) { c.Worktree = "/private/other" },
		"ref":             func(c *gitops.CandidateReceipt) { c.PrivateRef = "refs/heads/main" },
		"input":           func(c *gitops.CandidateReceipt) { c.OrderedInputOIDs = []string{strings.Repeat("e", 40)} },
		"file-scope":      func(c *gitops.CandidateReceipt) { c.Changes[0].Path = "outside.py" },
		"blob-object":     func(c *gitops.CandidateReceipt) { c.Changes[0].BlobOID = c.BaseOID },
		"file-mode":       func(c *gitops.CandidateReceipt) { c.Changes[0].Mode = "100755" },
		"deletion": func(c *gitops.CandidateReceipt) {
			c.Changes[0].Deleted = true
			c.Changes[0].BlobOID = ""
			c.Changes[0].Mode = ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newOwnerReworkFixture(t, mutate)
			if _, err := f.db.QueueRequestChanges(context.Background(), f.spec); err == nil {
				t.Fatal("mismatched Host candidate accepted")
			}
		})
	}
}

func TestOwnerRequestChangesRevalidatesExactSourceTuple(t *testing.T) {
	for name, mutate := range map[string]func(*OwnerReworkSpec){
		"event":          func(s *OwnerReworkSpec) { s.EventID = "other" },
		"event-revision": func(s *OwnerReworkSpec) { s.EventRevision++ },
		"hash":           func(s *OwnerReworkSpec) { s.EventHash = strings.Repeat("f", 64) },
		"slot":           func(s *OwnerReworkSpec) { s.ActionSlot = "other" },
		"segment":        func(s *OwnerReworkSpec) { s.SegmentID = "other" },
		"revision":       func(s *OwnerReworkSpec) { s.WorkRevision++ },
		"candidate":      func(s *OwnerReworkSpec) { s.CandidateOID = strings.Repeat("f", 40) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newOwnerReworkFixture(t)
			spec := f.spec
			mutate(&spec)
			if _, err := f.db.QueueRequestChanges(context.Background(), spec); err == nil {
				t.Fatal("source tuple was silently replaced")
			}
		})
	}
}

func commitOwnerNextCandidate(t *testing.T, f ownerReworkFixture, g contract.LaunchCommand) OwnerReworkSpec {
	t.Helper()
	ctx := context.Background()
	c := freezeOwnerFixtureCandidate(t, filepath.Dir(f.path), g)
	oid := c.CandidateOID
	body, err := gitops.CandidateArtifact("revised", c)

	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(f.path), "candidate-"+g.AttemptID+".json")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	var seq int64
	if err = f.db.sql.QueryRow(`SELECT MAX(sequence) FROM runtime_events WHERE producer_id=?`, f.host).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	e := contract.Event{Version: 1, ProducerID: f.host, EventID: g.SegmentID + "-result", RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, ExecutionEpoch: 1, CommandID: g.CommandID, Sequence: seq + 1, Kind: contract.EventResult, PayloadHash: runtimeHash(string(body)), Artifact: &contract.ArtifactRef{ID: g.SegmentID, Path: path, Size: int64(len(body)), SHA256: runtimeHash(string(body))}}
	if _, err = f.db.CommitHostEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	done := e
	done.Kind = contract.EventExited
	done.EventID = g.SegmentID + "-exited"
	done.Sequence++
	done.ActiveMS = 100
	zero := 0
	done.ExitCode = &zero
	if _, err = f.db.CommitHostEvent(ctx, done); err != nil {
		t.Fatal(err)
	}
	page, err := f.db.CollectPending(ctx, "edit", "", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range page.Events {
		if current.SegmentID == g.SegmentID {
			e = current
		}
	}
	return OwnerReworkSpec{TaskID: "edit", WorkRevision: g.WorkRevision, EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, SegmentID: g.SegmentID, CandidateOID: oid, Feedback: "next fix", Acceptance: "new tests", CommandID: g.SegmentID + "-owner-change"}
}
func TestOwnerRequestChangesNeverReplenishesAttemptsOrActiveBudget(t *testing.T) {
	f := newOwnerReworkFixture(t)
	ctx := context.Background()
	if _, err := f.db.QueueRequestChanges(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	second, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	nextSpec := commitOwnerNextCandidate(t, f, second)
	if _, err = f.db.QueueRequestChanges(ctx, nextSpec); err != nil {
		t.Fatal(err)
	}
	third, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	finalSpec := commitOwnerNextCandidate(t, f, third)
	if _, err = f.db.QueueRequestChanges(ctx, finalSpec); err == nil || err.Error() != "attempt_limit" {
		t.Fatalf("fourth attempt admitted: %v", err)
	}
	var attempts, revocations int
	var used int64
	if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM attempts WHERE task_id='edit'`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err = f.db.sql.QueryRow(`SELECT SUM(used_ms) FROM budget_runtime WHERE budget_group_id='edit'`).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM report_capabilities WHERE task_id='edit' AND status='revoked'`).Scan(&revocations); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || used != 300 || revocations != 2 || second.BudgetGroupID != f.grant.BudgetGroupID || third.BudgetGroupID != f.grant.BudgetGroupID || third.GrantedActiveMS != f.grant.GrantedActiveMS {
		t.Fatalf("budget/topup mismatch attempts=%d used=%d revoked=%d", attempts, used, revocations)
	}
	summary, err := f.db.SummarizeRun(ctx, "edit", "owner", f.token)
	if err != nil || summary.Tasks[0].ReviewReworkCount != 2 || summary.Tasks[0].TechnicalRetryCount != 0 || summary.Tasks[0].RequestChangesBlockedReason != "attempt_limit" {
		t.Fatalf("bad projected counts %+v %v", summary, err)
	}
}

func ownerFixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("/usr/bin/git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}
func freezeOwnerFixtureCandidate(t *testing.T, root string, g contract.LaunchCommand) gitops.CandidateReceipt {
	t.Helper()
	p, err := adapter.DecodeInvocationPayload(g.AdapterPayload)
	if err != nil {
		t.Fatal(err)
	}
	work := p.Directory
	if p.CandidateWorkspace.AutoDirectory {
		work += ".attempt-" + runtimeHash(g.RunID + "\x00" + g.TaskID + "\x00" + g.AttemptID + "\x00" + g.SegmentID)[:24]
	}
	journal, err := gitopsworker.OpenJournal(filepath.Join(root, "journal-"+g.AttemptID))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	ctx := gitops.WithCandidateJournal(context.Background(), journal)
	binding := gitops.CandidateBinding{RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, PlanRevision: g.PlanRevision}
	if p.CandidateWorkspace.Rework != nil {
		binding.RevisionSHA256 = p.CandidateWorkspace.Rework.Digest()
	}
	key := runtimeHash(g.RunID + "\x00" + g.TaskID + "\x00" + g.AttemptID + "\x00" + g.SegmentID)
	prepared, err := gitops.PrepareWorkspace(ctx, gitops.MaterializeRequest{RepoRoot: p.CandidateWorkspace.RepoRoot, Worktree: work, AttemptID: g.AttemptID, BaseOID: p.CandidateWorkspace.BaseOID, CandidateOID: p.CandidateWorkspace.BaseOID, OrderedInputOIDs: []string{p.CandidateWorkspace.BaseOID}, PlanRevision: int64(g.PlanRevision)}, binding, p.CandidateWorkspace.Paths, "refs/orchestrator/g3/candidate-"+key, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err = journal.RecordWorkspacePrepared(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(work, "calc.py"), []byte(fmt.Sprintf("revision %d\n", g.WorkRevision)), 0644); err != nil {
		t.Fatal(err)
	}
	c, err := gitops.FreezeWorkspace(ctx, prepared, journal)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestOwnerRequestChangesRejectsUnverifiableArtifactProjection(t *testing.T) {
	for _, kind := range []string{"digest", "legacy", "identity"} {
		t.Run(kind, func(t *testing.T) {
			f := newOwnerReworkArtifactFixture(t, func(value map[string]any) {
				switch kind {
				case "digest":
					value["candidate_projection_sha256"] = strings.Repeat("f", 64)
				case "legacy":
					delete(value, "candidate_projection_sha256")
					delete(value, "candidate_projection_version")
				case "identity":
					delete(value["candidate"].(map[string]any), "repo_identity")
					var c gitops.CandidateReceipt
					raw, _ := json.Marshal(value["candidate"])
					_ = json.Unmarshal(raw, &c)
					value["candidate_projection_sha256"] = gitops.CandidateProjectionDigest(c)
				}
			})
			if _, err := f.db.QueueRequestChanges(context.Background(), f.spec); err == nil {
				t.Fatal("unverified candidate projection admitted")
			}
			var revision, decisions int
			if err := f.db.sql.QueryRow(`SELECT work_revision,(SELECT COUNT(*) FROM retry_decisions) FROM task_runtime WHERE task_id='edit'`).Scan(&revision, &decisions); err != nil || revision != 1 || decisions != 0 {
				t.Fatalf("failed verification mutated revision/ledger: %d %d %v", revision, decisions, err)
			}
		})
	}
}
func TestOwnerRequestChangesRechecksActualPrivateRef(t *testing.T) {
	f := newOwnerReworkFixture(t)
	raw, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	var artifact struct {
		Candidate gitops.CandidateReceipt `json:"candidate"`
	}
	if err = json.Unmarshal(raw, &artifact); err != nil {
		t.Fatal(err)
	}
	ownerFixtureGit(t, artifact.Candidate.RepoRoot, "update-ref", artifact.Candidate.PrivateRef, artifact.Candidate.BaseOID)
	if _, err = f.db.QueueRequestChanges(context.Background(), f.spec); err == nil {
		t.Fatal("drifted candidate private ref admitted")
	}
}
func TestOwnerRequestChangesSurvivesOldWorktreeReclamation(t *testing.T) {
	f := newOwnerReworkFixture(t)
	raw, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	var artifact struct {
		Candidate gitops.CandidateReceipt `json:"candidate"`
	}
	if err = json.Unmarshal(raw, &artifact); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(artifact.Candidate.Worktree); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.QueueRequestChanges(context.Background(), f.spec); err != nil {
		t.Fatalf("immutable candidate unnecessarily depends on old worktree: %v", err)
	}
	if _, err = os.Stat(artifact.Candidate.Worktree); !os.IsNotExist(err) {
		t.Fatal("verification recreated old worktree")
	}
}
