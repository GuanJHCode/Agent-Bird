package store

import (
	"context"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func ownerLaunchSupervisor(f originalOwnerFixture) contract.OriginalOwnerLaunchSupervisor {
	return contract.OriginalOwnerLaunchSupervisor{Epoch: 1, PID: 700, Birth: "coordinator", Nonce: strings.Repeat("a", 64), ExecutableSHA256: f.prepare.ExecutableSHA256}
}

func ownerLaunchStoredBytes(t *testing.T, f originalOwnerFixture) int64 {
	t.Helper()
	var escrow int64
	if err := f.db.sql.QueryRow(`SELECT COALESCE(SUM(reserved_bytes),0) FROM owner_launch_escrows`).Scan(&escrow); err != nil {
		t.Fatal(err)
	}
	rows, err := f.db.sql.Query(`SELECT * FROM owner_launch_receipts WHERE phase='prepared_not_started'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, _ := rows.Columns()
	var preparedReceipts int64
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		preparedReceipts += 40
		for _, value := range values {
			if text, ok := value.(string); ok {
				preparedReceipts += int64(len(text))
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return originalOwnerCountStoredText(t, f, "owner_launch_attempts", 72) +
		originalOwnerCountStoredText(t, f, "owner_launch_heads", 24) +
		preparedReceipts +
		originalOwnerCountStoredText(t, f, "owner_launch_escrows", 16) + escrow
}

func ownerLaunchUsage(t *testing.T, f originalOwnerFixture) int64 {
	t.Helper()
	var run, global int64
	if err := f.db.sql.QueryRow(`SELECT COALESCE((SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='owner-run'),0),COALESCE((SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''),0)`).Scan(&run, &global); err != nil {
		t.Fatal(err)
	}
	if run != global {
		t.Fatalf("run/global usage drift: %d/%d", run, global)
	}
	return run
}

func TestOwnerLaunchQuotaAccountsRowsAndEscrow(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	ctx := context.Background()
	before := ownerLaunchUsage(t, f)
	p, err := f.db.PrepareSupervisedOriginalOwner(ctx, f.prepare, "", ownerLaunchSupervisor(f), f.key)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ownerLaunchUsage(t, f)-before, originalOwnerCountStoredText(t, f, "execution_owners", 72)+ownerLaunchStoredBytes(t, f); got != want {
		t.Fatalf("launch quota=%d, want every retained owner-launch row plus escrow=%d", got, want)
	}
	if _, err = f.db.RecordOriginalOwnerLaunchStartIntent(ctx, p.Intent.Scope.OwnerID, p.LaunchAttempt.AttemptSHA256, ownerLaunchSupervisor(f), f.key); err != nil {
		t.Fatal(err)
	}
	child := contract.OriginalOwnerTransport{PID: 202, Birth: "original-owner-birth"}
	if _, err = f.db.RecordOriginalOwnerLaunchStarted(ctx, p.Intent.Scope.OwnerID, p.LaunchAttempt.AttemptSHA256, ownerLaunchSupervisor(f), child, true, f.key); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.RecordOriginalOwnerLaunchWait(ctx, p.Intent.Scope.OwnerID, p.LaunchAttempt.AttemptSHA256, ownerLaunchSupervisor(f), child, OriginalOwnerLaunchExit{Signal: "SIGKILL"}, f.key); err != nil {
		t.Fatal(err)
	}
	if got, want := ownerLaunchUsage(t, f)-before, originalOwnerCountStoredText(t, f, "execution_owners", 72)+ownerLaunchStoredBytes(t, f); got != want {
		t.Fatalf("receipt quota=%d, want retained rows plus one escrow=%d", got, want)
	}
}

func TestOwnerLaunchPrepareQuotaOneByteBoundaryRollsBack(t *testing.T) {
	ctx := context.Background()
	probe := newOriginalOwnerFixture(t)
	before := ownerLaunchUsage(t, probe)
	if _, err := probe.db.PrepareSupervisedOriginalOwner(ctx, probe.prepare, "", ownerLaunchSupervisor(probe), probe.key); err != nil {
		t.Fatal(err)
	}
	growth := ownerLaunchUsage(t, probe) - before
	for _, short := range []int64{1, 0} {
		f := newOriginalOwnerFixture(t)
		f.db.runControlLimit, f.db.userControlLimit = growth-short, growth-short
		_, err := f.db.PrepareSupervisedOriginalOwner(ctx, f.prepare, "", ownerLaunchSupervisor(f), f.key)
		if short == 1 {
			if err != CodeError("control_spool_full") || ownerLaunchUsage(t, f) != 0 {
				t.Fatalf("one-byte-short err=%v usage=%d", err, ownerLaunchUsage(t, f))
			}
			var rows int
			if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM execution_owners`).Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("rejected admission retained rows=%d err=%v", rows, err)
			}
		} else if err != nil || ownerLaunchUsage(t, f) != growth {
			t.Fatalf("exact boundary err=%v usage=%d want=%d", err, ownerLaunchUsage(t, f), growth)
		}
	}
}

func TestOwnerLaunchStartedReplayAndWrongWaitChild(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	ctx := context.Background()
	p, err := f.db.PrepareSupervisedOriginalOwner(ctx, f.prepare, "", ownerLaunchSupervisor(f), f.key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.RecordOriginalOwnerLaunchStartIntent(ctx, p.Intent.Scope.OwnerID, p.LaunchAttempt.AttemptSHA256, ownerLaunchSupervisor(f), f.key); err != nil {
		t.Fatal(err)
	}
	child := contract.OriginalOwnerTransport{PID: 202, Birth: "original-owner-birth"}
	started, err := f.db.RecordOriginalOwnerLaunchStarted(ctx, p.Intent.Scope.OwnerID, p.LaunchAttempt.AttemptSHA256, ownerLaunchSupervisor(f), child, true, f.key)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.db.RecordOriginalOwnerLaunchStarted(ctx, p.Intent.Scope.OwnerID, p.LaunchAttempt.AttemptSHA256, ownerLaunchSupervisor(f), child, true, f.key)
	if err != nil || replay.ReceiptSHA256 != started.ReceiptSHA256 {
		t.Fatalf("exact started replay=%+v err=%v, want original receipt", replay, err)
	}
	if _, err = f.db.RecordOriginalOwnerLaunchWait(ctx, p.Intent.Scope.OwnerID, p.LaunchAttempt.AttemptSHA256, ownerLaunchSupervisor(f), contract.OriginalOwnerTransport{PID: 303, Birth: "wrong-child"}, OriginalOwnerLaunchExit{Signal: "SIGKILL"}, f.key); err != CodeError("original_owner_rejected") {
		t.Fatalf("wrong Wait child err=%v", err)
	}
}
