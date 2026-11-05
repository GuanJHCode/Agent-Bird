package store

import (
	"codex-cli-orchestration-design/tools/orchestrator/internal/gitops"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSummaryVerifierReleasesWriterAndConnection(t *testing.T) {
	f := newOwnerReworkFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	journal := &summaryBlockingGitJournal{entered: entered, release: release}

	done := make(chan error, 1)
	go func() {
		_, err := f.db.SummarizeRun(gitops.WithCandidateJournal(context.Background(), journal), "edit", "owner", f.token)
		done <- err
	}()
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("inspect did not enter unlocked verifier")
	}
	written := make(chan error, 1)
	go func() {
		err := f.db.CreateRun(context.Background(), RunSpec{ID: "run-b", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin-b", OriginPID: 1, OriginBirth: "birth"})
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run B write blocked by run A verifier")
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// The verifier is paused before any external evidence work. Mutations must
// finish during that pause, and their final DB state must replace old actions.
func TestSummaryDiscardsChangedFacts(t *testing.T) {
	for _, kind := range []string{"accept", "rework", "budget", "provider", "host", "adapter", "event", "owner"} {
		t.Run(kind, func(t *testing.T) {
			f := newOwnerReworkFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			f.db.summaryVerify = func(ctx context.Context) error {
				close(entered)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			type result struct {
				summary RunSummary
				err     error
			}
			done := make(chan result, 1)
			go func() {
				s, e := f.db.SummarizeRun(context.Background(), "edit", "owner", f.token)
				done <- result{s, e}
			}()
			<-entered
			switch kind {
			case "accept":
				_, err := f.db.ReviewResult(context.Background(), ReviewSpec{TaskID: "edit", WorkRevision: 1, EventID: f.spec.EventID, EventRevision: f.spec.EventRevision, EventHash: f.spec.EventHash, ActionSlot: f.spec.ActionSlot, Decision: "accept", CommandID: "accept"})
				if err != nil {
					t.Fatal(err)
				}
			case "rework":
				if _, err := f.db.QueueRequestChanges(context.Background(), f.spec); err != nil {
					t.Fatal(err)
				}
			default:
				statements := map[string]string{
					"budget":   `UPDATE budget_runtime SET used_ms=7200000`,
					"provider": `INSERT INTO provider_settings VALUES('owner','claude-code',0,NULL)`,
					"host":     `UPDATE runtime_hosts SET status='offline'`,
					"adapter":  `UPDATE task_runtime SET adapter_payload=json_set(adapter_payload,'$.session_id','changed')`,
					"event":    `UPDATE runtime_events SET event_revision=event_revision+1 WHERE event_id='candidate'`,
					"owner":    `UPDATE run_control SET token_hash='changed'`,
				}
				f.db.mu.Lock()
				_, err := f.db.sql.Exec(statements[kind])
				f.db.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			unblock()
			r := <-done
			if kind == "owner" {
				if r.err == nil {
					t.Fatal("owner revocation ignored")
				}
				return
			}
			if r.err != nil {
				t.Fatal(r.err)
			}
			task := r.summary.Tasks[0]
			if !slices.Equal(task.AllowedActions, []string{"status", "collect"}) || task.ReviewableResult != nil {
				t.Fatalf("stale projection: %+v", task)
			}
			old := f.spec
			old.CommandID = "stale-command"
			if _, err := f.db.QueueRequestChanges(context.Background(), old); err == nil {
				t.Fatal("stale tuple/admission accepted")
			}
		})
	}
}

func TestSummaryCandidateSurvivesUnavailableAdmission(t *testing.T) {
	for _, statement := range []string{`UPDATE budget_runtime SET used_ms=7200000`, `INSERT INTO provider_settings VALUES('owner','claude-code',0,NULL)`, `UPDATE runtime_hosts SET status='offline'`} {
		t.Run(statement, func(t *testing.T) {
			f := newOwnerReworkFixture(t)
			if _, err := f.db.sql.Exec(statement); err != nil {
				t.Fatal(err)
			}
			s, err := f.db.SummarizeRun(context.Background(), "edit", "owner", f.token)
			if err != nil {
				t.Fatal(err)
			}
			task := s.Tasks[0]
			if task.ReviewableResult == nil || task.ReviewableResult.CandidateOID != f.spec.CandidateOID || slices.Contains(task.AllowedActions, "request-changes") {
				t.Fatalf("candidate/admission conflated: %+v", task)
			}
		})
	}
}

func TestSummaryVerificationBoundAndCancellation(t *testing.T) {
	f := newOwnerReworkFixture(t)
	var active, maximum atomic.Int32
	entered := make(chan struct{}, 8)
	f.db.summaryVerify = func(ctx context.Context) error {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 8)
	for range 8 {
		go func() { _, err := f.db.SummarizeRun(ctx, "edit", "owner", f.token); done <- err }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("two verifier slots did not start")
		}
	}
	// A third verifier cannot enter while both slots remain occupied.
	select {
	case <-entered:
		t.Fatal("unbounded verification")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	for range 8 {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("canceled inspect leaked")
		}
	}
	if maximum.Load() != 2 || active.Load() != 0 || len(f.db.summarySlots) != 0 || f.db.sql.Stats().InUse != 0 {
		t.Fatalf("leaked resources: max=%d active=%d slots=%d sql=%+v", maximum.Load(), active.Load(), len(f.db.summarySlots), f.db.sql.Stats())
	}
	assertSummaryReadOnly(t, f)
}

func assertSummaryReadOnly(t *testing.T, f ownerReworkFixture) {
	t.Helper()
	var status string
	var revision, decisions int
	if err := f.db.sql.QueryRow(`SELECT t.status,r.work_revision,(SELECT COUNT(*) FROM review_decisions)+(SELECT COUNT(*) FROM retry_decisions) FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id='edit'`).Scan(&status, &revision, &decisions); err != nil {
		t.Fatal(err)
	}
	if status != "result_ready" || revision != 1 || decisions != 0 {
		t.Fatalf("inspect mutated state: %s/%d/%d", status, revision, decisions)
	}
}

type summaryGitJournal struct {
	cancel  context.CancelFunc
	records []gitops.GitProcessRecord
}

func (j *summaryGitJournal) RecordGitProcess(_ context.Context, r gitops.GitProcessRecord) error {
	j.records = append(j.records, r)
	if r.Phase == "spawned" {
		j.cancel()
	}
	return nil
}
func TestSummaryCancelsActualGitProcess(t *testing.T) {
	f := newOwnerReworkFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	journal := &summaryGitJournal{cancel: cancel}
	_, err := f.db.SummarizeRun(gitops.WithCandidateJournal(ctx, journal), "edit", "owner", f.token)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel Git: %v", err)
	}
	spawned, exited := 0, 0
	for _, r := range journal.records {
		switch r.Phase {
		case "spawned":
			spawned++
		case "exited":
			exited++
			if err := process.ConfirmGroupExited(r.Process.PGID); err != nil {
				t.Fatalf("Git group leaked: %v", err)
			}
		}
	}
	if spawned != 1 || exited != 1 {
		t.Fatalf("Git lifecycle: %+v", journal.records)
	}
	if f.db.sql.Stats().InUse != 0 || len(f.db.summarySlots) != 0 {
		t.Fatal("cancel retained resources")
	}
	assertSummaryReadOnly(t, f)
}

func TestSummaryGitFailureDoesNotMutate(t *testing.T) {
	f := newOwnerReworkFixture(t)
	body, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	var artifact struct {
		Candidate gitops.CandidateReceipt `json:"candidate"`
	}
	if err = json.Unmarshal(body, &artifact); err != nil {
		t.Fatal(err)
	}
	ownerFixtureGit(t, artifact.Candidate.RepoRoot, "update-ref", "-d", artifact.Candidate.PrivateRef)
	s, err := f.db.SummarizeRun(context.Background(), "edit", "owner", f.token)
	if err != nil {
		t.Fatal(err)
	}
	task := s.Tasks[0]
	if slices.Contains(task.AllowedActions, "request-changes") || task.ReviewableResult.CandidateOID != "" {
		t.Fatalf("invalid Git projected: %+v", task)
	}
	assertSummaryReadOnly(t, f)
	if f.db.sql.Stats().InUse != 0 || len(f.db.summarySlots) != 0 {
		t.Fatal("failure retained resources")
	}
}

// Block inside an actual Git lifecycle callback, so READ-01 cannot pass merely
// because a test hook happened outside the real verification path.
type summaryBlockingGitJournal struct {
	entered, release chan struct{}
	once             sync.Once
}

func (j *summaryBlockingGitJournal) RecordGitProcess(_ context.Context, r gitops.GitProcessRecord) error {
	if r.Phase == "spawned" {
		j.once.Do(func() { close(j.entered); <-j.release })
	}
	return nil
}
