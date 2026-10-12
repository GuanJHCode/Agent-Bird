package store

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

// Count all stored TEXT values independently of the admission helper's lists.
func originalOwnerCountStoredText(t *testing.T, f originalOwnerFixture, table string, fixed int64) int64 {
	t.Helper()
	rows, err := f.db.sql.Query(`SELECT * FROM ` + table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var total int64
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for n := range values {
			ptrs[n] = &values[n]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		total += fixed
		for _, v := range values {
			switch v := v.(type) {
			case string:
				total += int64(len(v))
			case []byte:
				total += int64(len(v))
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return total
}
func originalOwnerUsed(t *testing.T, f originalOwnerFixture) int64 {
	t.Helper()
	var run, global int64
	if err := f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='owner-run'`).Scan(&run); err != nil {
		t.Fatal(err)
	}
	if err := f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&global); err != nil {
		t.Fatal(err)
	}
	if run != global {
		t.Fatal("owner quota run/global divergence")
	}
	return run
}

func TestOriginalOwnerQuotaChargesCompleteRowsAndCombinedBindGrowth(t *testing.T) {
	probe := newOriginalOwnerFixture(t)
	ctx := context.Background()
	intent, err := probe.db.PrepareOriginalOwner(ctx, probe.prepare)
	if err != nil {
		t.Fatal(err)
	}
	before := originalOwnerUsed(t, probe)
	if before != originalOwnerCountStoredText(t, probe, "execution_owners", 72) {
		t.Fatal("intent quota omits stored values")
	}
	if _, err := probe.db.BindOriginalOwner(ctx, probe.bindSpec(intent), probe.key); err != nil {
		t.Fatal(err)
	}
	owner := originalOwnerCountStoredText(t, probe, "execution_owners", 72)
	journal := originalOwnerCountStoredText(t, probe, "owner_control_changes", 48)
	if originalOwnerUsed(t, probe) != owner+journal {
		t.Fatal("binding or journal storage escaped admission counters")
	}
	growth := owner - before
	if journal > growth {
		growth = journal
	}
	f := newOriginalOwnerFixture(t)
	i, err := f.db.PrepareOriginalOwner(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	used := originalOwnerUsed(t, f)
	f.db.runControlLimit = used + growth + 64
	if f.db.runControlLimit >= used+owner-before+journal-128 {
		t.Fatal("fixture must fit each single write but reject their combined cost")
	}
	original := originalOwnerSnapshot(t, f)
	if _, err := f.db.BindOriginalOwner(ctx, f.bindSpec(i), f.key); err != CodeError("control_spool_full") {
		t.Fatalf("combined write quota bypass: %v", err)
	}
	if original != originalOwnerSnapshot(t, f) || used != originalOwnerUsed(t, f) {
		t.Fatal("quota refusal partially bound owner")
	}
	f.db.runControlLimit = 16 << 20
	b, err := f.db.BindOriginalOwner(ctx, f.bindSpec(i), f.key)
	if err != nil {
		t.Fatal(err)
	}
	f.db.BeginCoordinatorEpoch(ctx, 2)
	f.db.RegisterHost(ctx, f.prepare.Host, 2)
	s := originalOwnerAttachInput(f, i, b, 2, 1, "renew")
	used = originalOwnerUsed(t, f)
	f.db.runControlLimit = used
	original = originalOwnerSnapshot(t, f)
	if _, err := f.db.CommitOriginalOwnerAttach(ctx, s, f.key); err != CodeError("control_spool_full") {
		t.Fatal("unbounded control journal growth", err)
	}
	if original != originalOwnerSnapshot(t, f) || used != originalOwnerUsed(t, f) {
		t.Fatal("attach quota failure changed lease")
	}
}

func TestOriginalOwnerInitialBindingAndAttachRollbackTogether(t *testing.T) {
	for _, phase := range []string{"bind", "attach"} {
		t.Run(phase, func(t *testing.T) {
			f := newOriginalOwnerFixture(t)
			ctx := context.Background()
			i, err := f.db.PrepareOriginalOwner(ctx, f.prepare)
			if err != nil {
				t.Fatal(err)
			}
			var binding OriginalOwnerBindResult
			if phase == "attach" {
				binding, err = f.db.BindOriginalOwner(ctx, f.bindSpec(i), f.key)
				if err != nil {
					t.Fatal(err)
				}
				f.db.BeginCoordinatorEpoch(ctx, 2)
				f.db.RegisterHost(ctx, f.prepare.Host, 2)
			}
			before := originalOwnerSnapshot(t, f)
			used := originalOwnerUsed(t, f)
			if _, err := f.db.sql.Exec(`CREATE TRIGGER owner_fixture_reject_journal BEFORE INSERT ON owner_control_changes BEGIN SELECT RAISE(ABORT,'owner_fixture_abort');END`); err != nil {
				t.Fatal(err)
			}
			if phase == "bind" {
				_, err = f.db.BindOriginalOwner(ctx, f.bindSpec(i), f.key)
			} else {
				_, err = f.db.CommitOriginalOwnerAttach(ctx, originalOwnerAttachInput(f, i, binding, 2, 1, "renew"), f.key)
			}
			if err == nil || !strings.Contains(err.Error(), "owner_fixture_abort") {
				t.Fatal("journal failure was not exercised", err)
			}
			if before != originalOwnerSnapshot(t, f) || used != originalOwnerUsed(t, f) {
				t.Fatal("permit failure left binding/lease/quota committed")
			}
			if _, err := f.db.sql.Exec(`DROP TRIGGER owner_fixture_reject_journal`); err != nil {
				t.Fatal(err)
			}
			if phase == "bind" {
				_, err = f.db.BindOriginalOwner(ctx, f.bindSpec(i), f.key)
			} else {
				_, err = f.db.CommitOriginalOwnerAttach(ctx, originalOwnerAttachInput(f, i, binding, 2, 1, "renew"), f.key)
			}
			if err != nil {
				t.Fatal("original request could not retry after rollback", err)
			}
		})
	}
}

func TestOriginalOwnerFirstBindRequiresSuccessfulCompareAndSwap(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	ctx := context.Background()
	i, err := f.db.PrepareOriginalOwner(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	before, used := originalOwnerSnapshot(t, f), originalOwnerUsed(t, f)
	if _, err := f.db.sql.Exec(`CREATE TRIGGER owner_fixture_ignore_bind BEFORE UPDATE ON execution_owners BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.BindOriginalOwner(ctx, f.bindSpec(i), f.key); err != ErrConflict {
		t.Fatalf("first permit committed without binding CAS: %v", err)
	}
	if before != originalOwnerSnapshot(t, f) || used != originalOwnerUsed(t, f) {
		t.Fatal("unsuccessful first binding left a journal/quota effect")
	}
}

func TestOriginalOwnerModeIsInitialInternalAdmissionOnly(t *testing.T) {
	for _, mode := range []string{"", "legacy", "supervised-v1", "unknown"} {
		t.Run("mode_"+mode, func(t *testing.T) {
			f := newOriginalOwnerFixture(t)
			ctx := context.Background()
			p := providerTestPlan("other", "controller", "grok-build")
			p.Host.ExecutionOwnerMode = mode
			r, err := f.db.SubmitPlan(ctx, p)
			if mode == "unknown" {
				if err != CodeError("execution_owner_mode_invalid") {
					t.Fatal(err)
				}
				var n int
				f.db.sql.QueryRow(`SELECT COUNT(*) FROM runs WHERE id='other'`).Scan(&n)
				if n != 0 {
					t.Fatal("invalid mode mutated admission")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := mode
			if want == "" {
				want = "legacy"
			}
			var got string
			f.db.sql.QueryRow(`SELECT execution_owner_mode FROM host_launches WHERE id=?`, r.LaunchID).Scan(&got)
			if got != want {
				t.Fatal("initial mode/default mismatch")
			}
			raw, _ := json.Marshal(p.Host)
			if strings.Contains(string(raw), "ExecutionOwnerMode") || strings.Contains(string(raw), "supervised-v1") {
				t.Fatal("internal mode leaked into public wire")
			}
			p.LaunchID = r.LaunchID
			p.Host.ExecutionOwnerMode = "supervised-v1"
			if _, err := f.db.SubmitPlan(ctx, p); err == nil {
				t.Fatal("repeated admission updated existing mode")
			}
			f.db.sql.QueryRow(`SELECT execution_owner_mode FROM host_launches WHERE id=?`, r.LaunchID).Scan(&got)
			if got != want {
				t.Fatal("conflict changed original mode")
			}
			// A new run cannot update/reuse another run's existing launch either.
			foreign := providerTestPlan("foreign", "controller", "grok-build")
			foreign.LaunchID, foreign.Host.ExecutionOwnerMode = r.LaunchID, "supervised-v1"
			if _, err := f.db.SubmitPlan(ctx, foreign); err == nil {
				t.Fatal("new run reused old launch and replaced its admission mode")
			}
			if err := f.db.sql.QueryRow(`SELECT execution_owner_mode FROM host_launches WHERE id=?`, r.LaunchID).Scan(&got); err != nil || got != want {
				t.Fatal("foreign launch conflict changed initial mode", err)
			}
			var leaked int
			if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM runs WHERE id='foreign'`).Scan(&leaked); err != nil || leaked != 0 {
				t.Fatal("foreign launch conflict partially admitted run", err)
			}
		})
	}
}

func TestOriginalOwnerLegacyAndExistingWorkersCannotAcquireOwner(t *testing.T) {
	for _, name := range []string{"legacy", "supervised_existing_segment"} {
		t.Run(name, func(t *testing.T) {
			f := newOriginalOwnerFixture(t)
			ctx := context.Background()
			if name == "legacy" {
				if _, err := f.db.sql.Exec(`UPDATE host_launches SET execution_owner_mode='legacy'`); err != nil {
					t.Fatal(err)
				}
			} else {
				// Recreate a historical pre-B2 row; the live B2 API now forbids this state.
				if _, err := f.db.sql.Exec(`UPDATE host_launches SET execution_owner_mode='legacy'; UPDATE runtime_hosts SET status='ready'`); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.ClaimReady(ctx, f.prepare.Host.LaunchID, 1, 1); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.sql.Exec(`UPDATE host_launches SET execution_owner_mode='supervised-v1'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.db.PrepareOriginalOwner(ctx, f.prepare); err == nil {
				t.Fatal("historical worker acquired original owner authority")
			}
			var n int
			f.db.sql.QueryRow(`SELECT COUNT(*) FROM execution_owners`).Scan(&n)
			if n != 0 {
				t.Fatal("failed enrollment occupied new authority")
			}
		})
	}
}

func TestOriginalOwnerOperationsDoNotAlterBusinessLedgers(t *testing.T) {
	f, i, b := originalOwnerBound(t)
	ctx := context.Background()
	apply := appliedFixture(t, f, b)
	if err := f.db.CommitOriginalOwnerApplied(ctx, apply, f.key.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
	if err := f.db.ActivateOriginalOwner(ctx, apply, f.key.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ClaimReady(ctx, i.Scope.ProducerID, 1, 1); err != nil {
		t.Fatal(err)
	}
	business := func() string {
		var out []string
		for _, table := range []string{"attempts", "segments", "segment_runtime", "budget_runtime", "budget_reservations", "task_runtime", "report_capabilities", "continuation_decisions"} {
			rows, err := f.db.sql.Query(`SELECT * FROM ` + table + ` ORDER BY 1`)
			if err != nil {
				t.Fatal(err)
			}
			cols, _ := rows.Columns()
			for rows.Next() {
				v := make([]any, len(cols))
				p := make([]any, len(cols))
				for n := range v {
					p[n] = &v[n]
				}
				if err := rows.Scan(p...); err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(v)
				out = append(out, table+string(raw))
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
		}
		return strings.Join(out, "\n")
	}
	before := business()
	f.db.BeginCoordinatorEpoch(ctx, 2)
	f.db.RegisterHost(ctx, f.prepare.Host, 2)
	if _, err := f.db.CommitOriginalOwnerAttach(ctx, originalOwnerAttachInput(f, i, b, 2, 1, "renew"), f.key); err != nil {
		t.Fatal(err)
	}
	if before != business() {
		t.Fatal("lease transition reset attempt/segment/reservation/report/continuation")
	}
	var secretRows int
	f.db.sql.QueryRow(`SELECT COUNT(*) FROM execution_owners WHERE instr(intent_json,?)>0 OR instr(COALESCE(original_binding_json,''),?)>0`, strings.Repeat("ab", 32), strings.Repeat("ab", 32)).Scan(&secretRows)
	if secretRows != 0 {
		t.Fatal("bootstrap plaintext persisted")
	}
}
