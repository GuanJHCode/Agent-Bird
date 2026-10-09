package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func requireOwnerLaunchRejected(t *testing.T, f originalOwnerFixture, owner string) {
	t.Helper()
	if _, err := f.db.InspectOriginalOwnerLaunch(context.Background(), owner); err != CodeError("original_owner_rejected") {
		t.Fatalf("InspectOriginalOwnerLaunch error=%v, want original_owner_rejected", err)
	}
}

func TestLoadOwnerLaunchTreatsOnlyAbsentSchema32FactsAsLegacy(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	ctx := context.Background()
	i, err := f.db.PrepareOriginalOwner(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.BindOriginalOwner(ctx, f.bindSpec(i), f.key); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP TABLE owner_launch_heads`,
		`DROP TABLE owner_launch_receipts`,
		`DROP TABLE owner_launch_escrows`,
		`DROP TABLE owner_launch_attempts`,
		`UPDATE schema_meta SET version=31`,
	} {
		if _, err = f.db.sql.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.db.InspectOriginalOwner(ctx, i.Scope.OwnerID); err != nil {
		t.Fatalf("pre-schema32 bound owner must retain legacy binding: %v", err)
	}
	if _, err = f.db.sql.Exec(`CREATE TABLE owner_launch_heads(owner_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.InspectOriginalOwner(ctx, i.Scope.OwnerID); err != CodeError("original_owner_rejected") {
		t.Fatalf("partial schema32 presence error=%v, want original_owner_rejected", err)
	}
}

func TestLoadOwnerLaunchRejectsCorruptV2FactMatrix(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(*testing.T, originalOwnerFixture, string)
	}{
		{
			name: "missing attempt one beneath head two",
			corrupt: func(t *testing.T, f originalOwnerFixture, owner string) {
				t.Helper()
				if _, err := f.db.sql.Exec(`DROP TRIGGER immutable_owner_launch_attempts_delete; DROP TRIGGER immutable_owner_launch_escrows_delete; DROP TRIGGER immutable_owner_launch_receipts_delete; DELETE FROM owner_launch_receipts WHERE owner_id=? AND attempt_hash=(SELECT attempt_hash FROM owner_launch_attempts WHERE owner_id=? AND attempt_no=1); DELETE FROM owner_launch_escrows WHERE owner_id=? AND attempt_no=1; DELETE FROM owner_launch_attempts WHERE owner_id=? AND attempt_no=1`, owner, owner, owner, owner); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "attempt scalar columns drift from signed body",
			corrupt: func(t *testing.T, f originalOwnerFixture, owner string) {
				t.Helper()
				if _, err := f.db.sql.Exec(`DROP TRIGGER immutable_owner_launch_attempts_update; UPDATE owner_launch_attempts SET supervisor_pid=999 WHERE owner_id=? AND attempt_no=2`, owner); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "attempt two predecessor does not name attempt one latest receipt",
			corrupt: func(t *testing.T, f originalOwnerFixture, owner string) {
				t.Helper()
				var attemptBody, receiptBody, oldHash string
				if err := f.db.sql.QueryRow(`SELECT a.attempt_json,r.receipt_json,a.attempt_hash FROM owner_launch_attempts a JOIN owner_launch_receipts r ON r.owner_id=a.owner_id AND r.attempt_hash=a.attempt_hash WHERE a.owner_id=? AND a.attempt_no=2`, owner).Scan(&attemptBody, &receiptBody, &oldHash); err != nil {
					t.Fatal(err)
				}
				var attempt contract.OriginalOwnerLaunchAttempt
				var receipt contract.OriginalOwnerLaunchReceipt
				if contract.DecodeOwnerRuntimeStrict([]byte(attemptBody), &attempt) != nil || contract.DecodeOwnerRuntimeStrict([]byte(receiptBody), &receipt) != nil {
					t.Fatal("fixture launch facts did not decode")
				}
				attempt.PriorReceiptSHA256 = strings.Repeat("9", 64)
				attempt, _ = contract.FinalizeOriginalOwnerLaunchAttempt(attempt)
				receipt.AttemptSHA256 = attempt.AttemptSHA256
				receipt, _ = contract.SignOriginalOwnerLaunchReceipt(receipt, f.key)
				attemptBody, _ = originalOwnerBody(attempt)
				receiptBody, _ = originalOwnerBody(receipt)
				if _, err := f.db.sql.Exec(`DROP TRIGGER immutable_owner_launch_attempts_update; DROP TRIGGER immutable_owner_launch_receipts_update; UPDATE owner_launch_attempts SET attempt_hash=?,attempt_json=? WHERE owner_id=? AND attempt_no=2; UPDATE owner_launch_receipts SET attempt_hash=?,receipt_json=?,receipt_hash=? WHERE owner_id=? AND attempt_hash=?; UPDATE owner_launch_heads SET latest_receipt_hash=? WHERE owner_id=?`, attempt.AttemptSHA256, attemptBody, owner, attempt.AttemptSHA256, receiptBody, receipt.ReceiptSHA256, owner, oldHash, receipt.ReceiptSHA256, owner); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "base and coordinator key columns drift",
			corrupt: func(t *testing.T, f originalOwnerFixture, owner string) {
				t.Helper()
				if _, err := f.db.sql.Exec(`DROP TRIGGER immutable_owner_launch_attempts_update; UPDATE owner_launch_attempts SET base_intent_json=attempt_json,supervisor_public_key=? WHERE owner_id=? AND attempt_no=2`, strings.Repeat("a", 64), owner); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "head phase drift",
			corrupt: func(t *testing.T, f originalOwnerFixture, owner string) {
				t.Helper()
				if _, err := f.db.sql.Exec(`UPDATE owner_launch_heads SET phase='waited' WHERE owner_id=?`, owner); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "receipt run drift",
			corrupt: func(t *testing.T, f originalOwnerFixture, owner string) {
				t.Helper()
				conn, err := f.db.sql.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if _, err = conn.ExecContext(context.Background(), `PRAGMA foreign_keys=OFF`); err != nil {
					t.Fatal(err)
				}
				defer conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`)
				if _, err = conn.ExecContext(context.Background(), `DROP TRIGGER immutable_owner_launch_receipts_update; UPDATE owner_launch_receipts SET run_id='proof-run-drift' WHERE owner_id=? AND receipt_no=2`, owner); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing attempt escrow",
			corrupt: func(t *testing.T, f originalOwnerFixture, owner string) {
				t.Helper()
				if _, err := f.db.sql.Exec(`DROP TRIGGER immutable_owner_launch_escrows_delete; DELETE FROM owner_launch_escrows WHERE owner_id=? AND attempt_no=2`, owner); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "orphan receipt",
			corrupt: func(t *testing.T, f originalOwnerFixture, owner string) {
				t.Helper()
				var receiptBody string
				if err := f.db.sql.QueryRow(`SELECT receipt_json FROM owner_launch_receipts WHERE owner_id=? AND receipt_no=2`, owner).Scan(&receiptBody); err != nil {
					t.Fatal(err)
				}
				var receipt contract.OriginalOwnerLaunchReceipt
				if contract.DecodeOwnerRuntimeStrict([]byte(receiptBody), &receipt) != nil {
					t.Fatal("fixture receipt did not decode")
				}
				receipt.AttemptSHA256 = strings.Repeat("e", 64)
				receipt, _ = contract.SignOriginalOwnerLaunchReceipt(receipt, f.key)
				receiptBody, _ = originalOwnerBody(receipt)
				if _, err := f.db.sql.Exec(`INSERT INTO owner_launch_receipts(owner_id,run_id,receipt_no,attempt_hash,phase,receipt_json,receipt_hash,created_at) VALUES(?,?,3,?,?,?,?,'proof-orphan')`, owner, "owner-run", receipt.AttemptSHA256, receipt.Phase, receiptBody, receipt.ReceiptSHA256); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, owner := ownerLaunchProofTwoAttemptFixture(t)
			tc.corrupt(t, f, owner)
			requireOwnerLaunchRejected(t, f, owner)
		})
	}
}

func ownerLaunchProofTwoAttemptFixture(t *testing.T) (originalOwnerFixture, string) {
	t.Helper()
	f := newOriginalOwnerFixture(t)
	ctx := context.Background()
	first, err := f.db.PrepareSupervisedOriginalOwner(ctx, f.prepare, "", ownerLaunchSupervisor(f), f.key)
	if err != nil {
		t.Fatal(err)
	}
	nextHost := f.prepare.Host
	nextHost.PID, nextHost.Birth = 303, "proof-retry-host"
	a, err := f.db.ResolvePreBindRetryAdmission(ctx, nextHost, 1, f.prepare.ExecutableSHA256)
	if err != nil {
		t.Fatal(err)
	}
	a.Supervisor = ownerLaunchSupervisor(f)
	a.Supervisor.PID, a.Supervisor.Birth, a.Supervisor.Nonce = 701, "proof-retry-supervisor", strings.Repeat("b", 64)
	a.Prepare = f.prepare
	a.Prepare.Host = nextHost
	a.Prepare.BootstrapID, a.Prepare.RequestID = "proof-retry-bootstrap", "proof-retry-request"
	a.Prepare.BootstrapTokenSHA256, _ = contract.OriginalOwnerBootstrapSHA256(strings.Repeat("cd", 32))
	a.Prepare.RequestSHA256 = strings.Repeat("d", 64)
	a.Prepare.StartupDeadlineUnixMS = time.Now().Add(time.Minute).UnixMilli()
	if _, err = f.db.CommitPreBindRetryAdmission(ctx, a, first.Intent.FirstHost, f.key); err != nil {
		t.Fatal(err)
	}
	return f, first.Intent.Scope.OwnerID
}
