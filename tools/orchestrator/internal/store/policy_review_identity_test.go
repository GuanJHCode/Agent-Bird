package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
)

// These are authenticated Store transition fixtures, not OS identity checks.
func TestPolicyReviewRetrySurvivesOnlySamePhysicalHostReconnect(t *testing.T) {
	for _, kind := range []string{"same_identity", "new_pid", "reused_pid", "offline"} {
		t.Run(kind, func(t *testing.T) {
			f := newReviewRetryPolicyFixture(t, adapter.ProviderClaude, true)
			ctx := context.Background()
			fixture := budgetBatchFixture{db: f.db, host: f.host, grant: f.grant, sequence: 4}
			if _, err := f.db.CommitHostEvent(ctx, policyRejection(&fixture, "probe_start_eagain")); err != nil {
				t.Fatal(err)
			}
			if err := f.db.BeginCoordinatorEpoch(ctx, 2); err != nil {
				t.Fatal(err)
			}
			if kind != "offline" {
				hello := f.hello
				if kind == "new_pid" {
					hello.PID++
				}
				if kind == "reused_pid" {
					hello.Birth = "different-process-birth"
				}
				host, err := f.db.RegisterHost(ctx, hello, 2)
				if err != nil || host != f.host {
					t.Fatal("host reconnect fixture", host, err)
				}
			}
			if _, err := f.db.sql.Exec(`UPDATE retry_decisions SET eligible_at_unix_ms=1 WHERE origin='policy'`); err != nil {
				t.Fatal(err)
			}
			next, err := f.db.ClaimReady(ctx, f.host, 2, 2)
			wantAttempts := 1
			if kind == "same_identity" {
				wantAttempts = 2
				if err != nil || next.AttemptID == f.grant.AttemptID || next.WorkRevision != f.grant.WorkRevision || !reflect.DeepEqual(next.Input, f.grant.Input) || !reflect.DeepEqual(next.DependencyInputs, f.grant.DependencyInputs) {
					t.Fatal("same physical Host lost frozen retry after coordinator restart", err)
				}
				if _, err = f.db.ClaimReady(ctx, f.host, 2, 2); err == nil {
					t.Fatal("retry dispatched twice")
				}
			} else if err == nil {
				t.Fatal("different or offline Host consumed frozen retry")
			}
			var attempts int
			if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM attempts WHERE task_id='review'`).Scan(&attempts); err != nil || attempts != wantAttempts {
				t.Fatal("claim changed attempt accounting", attempts, wantAttempts, err)
			}
		})
	}
}
