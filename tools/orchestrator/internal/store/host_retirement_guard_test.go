package store

import (
	"context"
	"errors"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestRetireHostRechecksRetryQueuedAfterEligibilityScan(t *testing.T) {
	f := c02RetirableReviewFixture(t)
	ctx := context.Background()
	ids, err := f.db.RetirableHosts(ctx)
	if err != nil || len(ids) != 1 || ids[0] != f.host {
		t.Fatalf("initial retirement candidates=%v err=%v", ids, err)
	}
	if _, err := f.db.QueueRetry(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	if err := f.db.RetireHost(ctx, ids[0]); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale eligibility retired newly queued review: err=%v", err)
	}
	var host, launch string
	if err := f.db.sql.QueryRow(`SELECT h.status,l.status FROM runtime_hosts h JOIN host_launches l ON l.host_id=h.id WHERE h.id=?`, f.host).Scan(&host, &launch); err != nil || host != "ready" || launch != "ready" {
		t.Fatalf("retirement refusal changed Host/launch: %s/%s err=%v", host, launch, err)
	}
}

func TestRetireHostCannotReleaseReconcilingSession(t *testing.T) {
	f := c02RetirableReviewFixture(t)
	ctx := context.Background()
	ids, err := f.db.RetirableHosts(ctx)
	if err != nil || len(ids) != 1 {
		t.Fatalf("initial retirement candidates=%v err=%v", ids, err)
	}
	if err := f.db.BeginHostReconcile(ctx, f.host); err != nil {
		t.Fatal(err)
	}
	if err := f.db.RetireHost(ctx, ids[0]); !errors.Is(err, ErrConflict) {
		t.Fatalf("retirement accepted a reconciling replacement: err=%v", err)
	}
	var host, launch string
	if err := f.db.sql.QueryRow(`SELECT h.status,l.status FROM runtime_hosts h JOIN host_launches l ON l.host_id=h.id WHERE h.id=?`, f.host).Scan(&host, &launch); err != nil || host != "reconciling" || launch != "reconciling" {
		t.Fatalf("retirement changed reconciling Host/launch: %s/%s err=%v", host, launch, err)
	}
}

func c02RetirableReviewFixture(t *testing.T) reviewRetryFixture {
	t.Helper()
	f := newReviewRetryFixture(t, contract.EventFailed)
	// Keep the existing settled failed-review event/input bindings while
	// seeding the valid terminal task projection eligible for retirement.
	// The normal fixture intentionally leaves a queued integrate successor.
	if _, err := f.db.RequestStop(context.Background(), "integrate", 1, "owner_cancel"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.sql.Exec(`UPDATE tasks SET status='failed' WHERE id='review'`); err != nil {
		t.Fatal(err)
	}
	return f
}
