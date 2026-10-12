package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestExistingOwnerResumeAcceptsOfflineCurrentEpochAndOldPermit(t *testing.T) {
	f, request, dead := ownerSuccessionResumeFixture(t)
	ctx := context.Background()
	if err := f.db.BeginCoordinatorEpoch(ctx, 2); err != nil {
		t.Fatal(err)
	}
	o := liveOwnerObservation(dead)
	o.Epoch = 2
	before := ownerSuccessionResumeTablesSnapshot(t, f.db, "execution_owners", "owner_control_changes", "runtime_hosts", "host_launches")
	r, err := f.db.ResumeExistingOriginalOwner(ctx, request, o, f.key.Public().(ed25519.PublicKey))
	if err != nil || r.CoordinatorEpoch != 2 {
		t.Fatal("current offline Host with old applied signer rejected", r, err)
	}
	if before != ownerSuccessionResumeTablesSnapshot(t, f.db, "execution_owners", "owner_control_changes", "runtime_hosts", "host_launches") {
		t.Fatal("live resume rewrote old owner or offline transport")
	}
}

func TestExistingOwnerResumeAuthenticatesBeforeHistory(t *testing.T) {
	for _, mode := range []string{"controller", "token", "rotated_token", "task"} {
		t.Run(mode, func(t *testing.T) {
			f, request, dead := ownerSuccessionResumeFixture(t)
			if _, err := f.db.ResumeExistingOriginalOwner(context.Background(), request, liveOwnerObservation(dead), f.key.Public().(ed25519.PublicKey)); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "controller":
				request.ControllerThread = "foreign"
			case "token":
				request.ControlToken = "foreign"
			case "rotated_token":
				if _, err := f.db.sql.Exec(`UPDATE run_control SET token_hash=? WHERE run_id='owner-run'`, runtimeHash("new-authority")); err != nil {
					t.Fatal(err)
				}
			case "task":
				request.TaskID = "foreign"
			}
			before := liveOwnerResumeSnapshot(t, f.db)
			r, found, err := f.db.LookupOriginalOwnerResume(context.Background(), request)
			want := CodeError("owner_mismatch")
			if mode == "task" {
				want = ErrNotFound
			}
			if !errors.Is(err, want) || found || r != (OriginalOwnerResumeReceipt{}) {
				t.Fatal("unauthenticated receipt disclosed", r, found, err)
			}
			if _, err = f.db.ResumeExistingOriginalOwner(context.Background(), request, OriginalOwnerLiveObservation{}, nil); !errors.Is(err, want) {
				t.Fatal("private replay skipped current authority", err)
			}
			if before != liveOwnerResumeSnapshot(t, f.db) {
				t.Fatal("unauthorized lookup wrote state")
			}
		})
	}
}

func TestExistingOwnerResumeRejectsMutableDrift(t *testing.T) {
	for _, change := range []string{"host_epoch", "host_status", "host_pid", "launch_host", "origin_pid", "origin_birth", "generation", "executable", "revision", "status", "foreign_signing_key"} {
		t.Run(change, func(t *testing.T) {
			f, request, dead := ownerSuccessionResumeFixture(t)
			pub := f.key.Public().(ed25519.PublicKey)
			q := map[string]string{
				"host_epoch":   `UPDATE runtime_hosts SET coordinator_epoch=2`,
				"host_status":  `UPDATE runtime_hosts SET status='unknown'`,
				"host_pid":     `UPDATE runtime_hosts SET pid=pid+1`,
				"launch_host":  `UPDATE host_launches SET host_id='foreign'`,
				"origin_pid":   `UPDATE runs SET origin_pid=origin_pid+1`,
				"origin_birth": `UPDATE runs SET origin_birth='foreign'`,
				"generation":   `UPDATE host_launches SET host_generation='foreign'; UPDATE runtime_hosts SET host_generation='foreign'`,
				"executable":   `UPDATE host_launches SET executable='/foreign'; UPDATE runtime_hosts SET executable='/foreign'`,
				"revision":     `UPDATE task_runtime SET work_revision=work_revision+1`,
				"status":       `UPDATE tasks SET status='running'`,
			}[change]
			if q != "" {
				if _, err := f.db.sql.Exec(q); err != nil {
					t.Fatal(err)
				}
			} else {
				pub = ed25519.NewKeyFromSeed([]byte(strings.Repeat("f", 32))).Public().(ed25519.PublicKey)
			}
			before := liveOwnerResumeSnapshot(t, f.db)
			if _, err := f.db.ResumeExistingOriginalOwner(context.Background(), request, liveOwnerObservation(dead), pub); err == nil {
				t.Fatal("drifted state admitted live queue")
			}
			if before != liveOwnerResumeSnapshot(t, f.db) {
				t.Fatal("drift rejection changed persistent state")
			}
		})
	}
}

func TestExistingOwnerResumeConcurrentCommandIdentity(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprint(mixed), func(t *testing.T) {
			f, request, dead := ownerSuccessionResumeFixture(t)
			ctx, pub := context.Background(), f.key.Public().(ed25519.PublicKey)
			var wg sync.WaitGroup
			start := make(chan struct{})
			type result struct {
				receipt OriginalOwnerResumeReceipt
				err     error
			}
			results := make(chan result, 8)
			for n := 0; n < 8; n++ {
				wg.Add(1)
				go func(n int) {
					defer wg.Done()
					<-start
					if mixed && n%2 == 0 {
						a, err := f.db.ResumeOriginalOwner(ctx, request, dead, pub)
						results <- result{ownerSuccessionResumeReceipt(a), err}
					} else {
						r, err := f.db.ResumeExistingOriginalOwner(ctx, request, liveOwnerObservation(dead), pub)
						results <- result{r, err}
					}
				}(n)
			}
			close(start)
			wg.Wait()
			close(results)
			var accepted OriginalOwnerResumeReceipt
			success, conflict := 0, 0
			for r := range results {
				if errors.Is(r.err, ErrConflict) {
					conflict++
					continue
				}
				if r.err != nil {
					t.Fatal(r.err)
				}
				if success != 0 && accepted != r.receipt {
					t.Fatal("concurrent command produced different receipts")
				}
				accepted = r.receipt
				success++
			}
			if (!mixed && (success != 8 || conflict != 0)) || (mixed && (success != 4 || conflict != 4)) {
				t.Fatal("unexpected concurrent admission", success, conflict)
			}
			if got := originalOwnerSchemaCount(t, f.db.sql, `SELECT (SELECT COUNT(*) FROM existing_owner_resume_commands)+(SELECT COUNT(*) FROM owner_succession_authorizations)`); got != 1 {
				t.Fatal("command was committed more than once", got)
			}
			r, found, err := f.db.LookupOriginalOwnerResume(ctx, request)
			if err != nil || !found || r != accepted {
				t.Fatal("concurrent receipt not durable", r, err)
			}
		})
	}
}

func TestExistingOwnerResumeExactUTF8QuotaBoundary(t *testing.T) {
	for _, scope := range []string{"run", "global"} {
		for _, deficit := range []int64{0, 1} {
			t.Run(fmt.Sprintf("%s-minus-%d", scope, deficit), func(t *testing.T) {
				f, request, dead := ownerSuccessionResumeFixture(t)
				const created = "2026-10-08T01:00:00.123456789Z"
				// Eight TEXT cells (including the empty no-segment snapshot), five integers.
				incoming := int64(5*8 + 64)
				for _, value := range []string{request.CommandID, "owner-run", request.TaskID, f.prepare.Host.LaunchID, dead.OwnerID, "", created} {
					incoming += int64(len([]byte(value)))
				}
				global, run := collectionUsage(t, f.db.sql, "global", ""), collectionUsage(t, f.db.sql, "run", "owner-run")
				if scope == "run" {
					f.db.runControlLimit = run + incoming - deficit
				} else {
					f.db.userControlLimit = global + incoming - deficit
				}
				before := liveOwnerResumeSnapshot(t, f.db)
				_, err := f.db.resumeExistingOriginalOwner(context.Background(), request, liveOwnerObservation(dead), f.key.Public().(ed25519.PublicKey), func() string { return created })
				if deficit != 0 {
					if err != CodeError("control_spool_full") || before != liveOwnerResumeSnapshot(t, f.db) {
						t.Fatal("one-byte shortage admitted or wrote state", err)
					}
				} else if err != nil || collectionUsage(t, f.db.sql, "global", "") != global+incoming || collectionUsage(t, f.db.sql, "run", "owner-run") != run+incoming {
					t.Fatal("exact UTF8 boundary failed", err)
				}
			})
		}
	}
}

func TestExistingOwnerResumeLateFailureAndCommitRollback(t *testing.T) {
	for _, stage := range []string{"queue", "receipt", "commit", "cancel"} {
		t.Run(stage, func(t *testing.T) {
			f, request, dead := ownerSuccessionResumeFixture(t)
			q := map[string]string{
				"queue":   `CREATE TRIGGER fail_live_queue BEFORE UPDATE OF ready_sequence ON task_runtime BEGIN SELECT RAISE(ABORT,'injected_queue'); END`,
				"receipt": `CREATE TRIGGER fail_live_receipt AFTER INSERT ON existing_owner_resume_commands BEGIN SELECT RAISE(ABORT,'injected_receipt'); END`,
				"commit":  `CREATE TABLE live_commit_fault(parent TEXT REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail_live_commit AFTER INSERT ON existing_owner_resume_commands BEGIN INSERT INTO live_commit_fault VALUES('missing'); END`,
			}[stage]
			if q != "" {
				if _, err := f.db.sql.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if stage == "cancel" {
				cancel()
			}
			before := liveOwnerResumeSnapshot(t, f.db)
			_, err := f.db.ResumeExistingOriginalOwner(ctx, request, liveOwnerObservation(dead), f.key.Public().(ed25519.PublicKey))
			if err == nil || (stage == "cancel" && !errors.Is(err, context.Canceled)) {
				t.Fatal("injected failure not reported", err)
			}
			if before != liveOwnerResumeSnapshot(t, f.db) {
				t.Fatal("failed live transaction leaked queue/quota/receipt")
			}
			if stage == "commit" {
				external, e := sql.Open("sqlite", f.db.path)
				if e != nil {
					t.Fatal(e)
				}
				defer external.Close()
				committed := originalOwnerSchemaCount(t, external, `SELECT COUNT(*) FROM existing_owner_resume_commands`)
				pooled := originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM existing_owner_resume_commands`)
				t.Logf("actual COMMIT failure=%v external=%d pooled=%d", err, committed, pooled)
				if committed != 0 || pooled != 0 || originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM live_commit_fault`) != 0 || originalOwnerSchemaCount(t, f.db.sql, `PRAGMA foreign_keys`) != 1 {
					t.Fatal("commit failure retained transaction or lost FK enforcement")
				}
				if _, e = f.db.sql.Exec(`DROP TRIGGER fail_live_commit`); e != nil {
					t.Fatal(e)
				}
				if _, e = f.db.ResumeExistingOriginalOwner(context.Background(), request, liveOwnerObservation(dead), f.key.Public().(ed25519.PublicKey)); e != nil {
					t.Fatal("same command could not retry after rolled back commit", e)
				}
			}
		})
	}
}
