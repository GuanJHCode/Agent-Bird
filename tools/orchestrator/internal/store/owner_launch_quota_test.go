package store

import (
	"context"
	"reflect"
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
	var headSlack int64
	if err := f.db.sql.QueryRow(`SELECT COALESCE(SUM(20-length(CAST(phase AS BLOB))+30-length(CAST(updated_at AS BLOB))),0) FROM owner_launch_heads`).Scan(&headSlack); err != nil {
		t.Fatal(err)
	}
	return originalOwnerCountStoredText(t, f, "owner_launch_attempts", 72) +
		originalOwnerCountStoredText(t, f, "owner_launch_heads", 24) + headSlack +
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
		prepared, err := f.db.PrepareSupervisedOriginalOwner(ctx, f.prepare, "", ownerLaunchSupervisor(f), f.key)
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
		} else {
			verifyOwnerLaunchReservedReceipts(t, f, prepared, f.prepare.Host, strings.Repeat("ab", 32))
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

// A valid Bind is another ordinary quota consumer. At the exact retained
// launch ceiling it cannot steal reserved receipt space, yet the original
// Start/Wait proof chain must still finish without allocating more quota.
func verifyOwnerLaunchReservedReceipts(t *testing.T, f originalOwnerFixture, prepared contract.OwnerPrepared, host contract.HostHello, token string) {
	t.Helper()
	ctx := context.Background()
	a := *prepared.LaunchAttempt
	ceiling := ownerLaunchUsage(t, f)
	if _, err := f.db.RecordOriginalOwnerLaunchStartIntent(ctx, a.BaseIntent.Scope.OwnerID, a.AttemptSHA256, a.Supervisor, f.key); err != nil {
		t.Fatal(err)
	}
	if got := ownerLaunchUsage(t, f); got != ceiling {
		t.Fatalf("starting released reserved head bytes: %d != %d", got, ceiling)
	}
	spec := f.bindSpec(prepared.Intent)
	spec.Host = host
	spec.Hello.BootstrapToken = token
	child := contract.OriginalOwnerTransport{PID: spec.Hello.Owner.PID, Birth: spec.Hello.Owner.Birth}
	if _, err := f.db.RecordOriginalOwnerLaunchStarted(ctx, a.BaseIntent.Scope.OwnerID, a.AttemptSHA256, a.Supervisor, child, true, f.key); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.BindOriginalOwner(ctx, spec, f.key); err != CodeError("control_spool_full") {
		t.Fatalf("ordinary Bind consumed receipt escrow: %v", err)
	}
	zero := 0
	if _, err := f.db.RecordOriginalOwnerLaunchWait(ctx, a.BaseIntent.Scope.OwnerID, a.AttemptSHA256, a.Supervisor, child, OriginalOwnerLaunchExit{Code: &zero}, f.key); err != nil {
		t.Fatal(err)
	}
	if got := ownerLaunchUsage(t, f); got != ceiling {
		t.Fatalf("prepaid receipt/head changed quota %d != %d", got, ceiling)
	}
	if got, want := ownerLaunchUsage(t, f), originalOwnerCountStoredText(t, f, "execution_owners", 72)+ownerLaunchStoredBytes(t, f); got != want {
		t.Fatalf("retained quota %d != actual rows and reservations %d", got, want)
	}
	state, err := f.db.InspectOriginalOwnerLaunch(ctx, a.BaseIntent.Scope.OwnerID)
	if err != nil || state.Phase != "intended" || state.Binding != nil || state.ControlRevision != 0 {
		t.Fatalf("failed Bind changed owner: %+v %v", state, err)
	}
}

func TestOwnerLaunchRetryQuotaOneByteBoundary(t *testing.T) {
	ctx := context.Background()
	setup := func() (originalOwnerFixture, PreBindRetryAdmission, contract.OwnerPrepared) {
		f := newOriginalOwnerFixture(t)
		p, err := f.db.PrepareSupervisedOriginalOwner(ctx, f.prepare, "", ownerLaunchSupervisor(f), f.key)
		if err != nil {
			t.Fatal(err)
		}
		h := f.prepare.Host
		h.PID = 303
		h.Birth = "replacement-主机"
		a, err := f.db.ResolvePreBindRetryAdmission(ctx, h, 1, f.prepare.ExecutableSHA256)
		if err != nil {
			t.Fatal(err)
		}
		a.Supervisor = ownerLaunchSupervisor(f)
		a.Prepare = f.prepare
		a.Prepare.Host = h
		a.Prepare.BootstrapID = "longer-retry-bootstrap"
		a.Prepare.RequestID = "longer-retry-request"
		a.Prepare.BootstrapTokenSHA256, _ = contract.OriginalOwnerBootstrapSHA256(strings.Repeat("cd", 32))
		a.Prepare.RequestSHA256 = strings.Repeat("d", 64)
		return f, a, p
	}
	probe, a, p := setup()
	before := ownerLaunchUsage(t, probe)
	if _, err := probe.db.CommitPreBindRetryAdmission(ctx, a, p.Intent.FirstHost, probe.key); err != nil {
		t.Fatal(err)
	}
	growth := ownerLaunchUsage(t, probe) - before
	for _, limitScope := range []string{"run", "global"} {
		t.Run(limitScope, func(t *testing.T) {
			f, a, p := setup()
			before := ownerLaunchUsage(t, f)
			old, err := f.db.InspectOriginalOwnerLaunch(ctx, p.Intent.Scope.OwnerID)
			if err != nil {
				t.Fatal(err)
			}
			binding, err := f.db.HostBinding(ctx, p.Intent.Scope.LaunchID)
			if err != nil {
				t.Fatal(err)
			}
			if limitScope == "run" {
				f.db.runControlLimit = before + growth - 1
			} else {
				f.db.userControlLimit = before + growth - 1
			}
			if _, err = f.db.CommitPreBindRetryAdmission(ctx, a, p.Intent.FirstHost, f.key); err != CodeError("control_spool_full") {
				t.Fatalf("one-byte-short retry admitted: %v", err)
			}
			after, err := f.db.InspectOriginalOwnerLaunch(ctx, p.Intent.Scope.OwnerID)
			if err != nil || !reflect.DeepEqual(old, after) || ownerLaunchUsage(t, f) != before {
				t.Fatalf("rejected retry changed proof/usage: %v", err)
			}
			afterHost, err := f.db.HostBinding(ctx, p.Intent.Scope.LaunchID)
			if err != nil || !reflect.DeepEqual(binding, afterHost) {
				t.Fatalf("rejected retry moved Host: %v", err)
			}
			var attempts, escrows int
			if err = f.db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM owner_launch_attempts),(SELECT COUNT(*) FROM owner_launch_escrows)`).Scan(&attempts, &escrows); err != nil || attempts != 1 || escrows != 1 {
				t.Fatalf("retry rollback left facts %d/%d %v", attempts, escrows, err)
			}
			f.db.runControlLimit = before + growth
			f.db.userControlLimit = before + growth
			second, err := f.db.CommitPreBindRetryAdmission(ctx, a, p.Intent.FirstHost, f.key)
			if err != nil || ownerLaunchUsage(t, f) != before+growth {
				t.Fatalf("exact retry quota: %v %d want %d", err, ownerLaunchUsage(t, f), before+growth)
			}
			verifyOwnerLaunchReservedReceipts(t, f, second, a.NewHost, strings.Repeat("cd", 32))
		})
	}
}

func TestOwnerLaunchSuccessorQuotaIncludesAuthorization(t *testing.T) {
	ctx := context.Background()
	probe, spec, _ := ownerSuccessionPrepareFixture(t, false)
	before := ownerLaunchUsage(t, probe)
	ownersBefore := originalOwnerCountStoredText(t, probe, "execution_owners", 72)
	if _, err := probe.db.PrepareSupervisedOriginalOwner(ctx, spec.PrepareOriginalOwnerSpec, spec.AuthorizationID, ownerLaunchSupervisor(probe), probe.key); err != nil {
		t.Fatal(err)
	}
	growth := ownerLaunchUsage(t, probe) - before
	// The additional nullable succession reference carries its existing 8-byte
	// metadata fee; its UTF-8 text is included by the independent row scan.
	if want := originalOwnerCountStoredText(t, probe, "execution_owners", 72) - ownersBefore + 8 + ownerLaunchStoredBytes(t, probe); growth != want {
		t.Fatalf("successor charge %d != actual rows/reserve %d", growth, want)
	}
	for _, limitScope := range []string{"run", "global"} {
		t.Run(limitScope, func(t *testing.T) {
			f, spec, _ := ownerSuccessionPrepareFixture(t, false)
			before := ownerLaunchUsage(t, f)
			snapshot := ownerSuccessionResumeSnapshot(t, f.db)
			if limitScope == "run" {
				f.db.runControlLimit = before + growth - 1
			} else {
				f.db.userControlLimit = before + growth - 1
			}
			if _, err := f.db.PrepareSupervisedOriginalOwner(ctx, spec.PrepareOriginalOwnerSpec, spec.AuthorizationID, ownerLaunchSupervisor(f), f.key); err != CodeError("control_spool_full") {
				t.Fatalf("one-byte-short successor: %v", err)
			}
			if ownerLaunchUsage(t, f) != before || ownerSuccessionResumeSnapshot(t, f.db) != snapshot {
				t.Fatal("rejected successor changed state or quota")
			}
			f.db.runControlLimit = before + growth
			f.db.userControlLimit = before + growth
			next, err := f.db.PrepareSupervisedOriginalOwner(ctx, spec.PrepareOriginalOwnerSpec, spec.AuthorizationID, ownerLaunchSupervisor(f), f.key)
			if err != nil || ownerLaunchUsage(t, f) != before+growth || next.Intent.Scope.Incarnation != 2 {
				t.Fatalf("exact successor admission: %v usage=%d", err, ownerLaunchUsage(t, f))
			}
		})
	}
}
