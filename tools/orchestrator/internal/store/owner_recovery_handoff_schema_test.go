package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

type recoveryHandoffSchemaRow struct {
	priorBinding, binding                                      string
	predecessorControl                                         int64
	owner, authorization, run, launch, producer, predecessor   string
	priorScope, scope, priorRecord, record, authority, created string
	epoch                                                      int64
}

func recoveryHandoffSchemaFixture(t *testing.T) (originalOwnerFixture, recoveryHandoffSchemaRow) {
	t.Helper()
	f, p, _ := ownerSuccessionPrepareFixture(t, false)
	i, err := f.db.PrepareSuccessorOwner(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.BindOriginalOwner(context.Background(), successorOwnerBindSpec(f, p, i), f.key); err != nil {
		t.Fatal(err)
	}
	r := recoveryHandoffSchemaRow{owner: i.Scope.OwnerID, authorization: p.AuthorizationID, run: i.Scope.RunID, launch: i.Scope.LaunchID, producer: i.Scope.ProducerID, priorScope: strings.Repeat("a", 64), scope: strings.Repeat("b", 64), priorRecord: strings.Repeat("c", 64), record: strings.Repeat("d", 64), authority: strings.Repeat("e", 64), created: "2026-10-08T01:00:00Z", epoch: 1}
	if err = f.db.sql.QueryRow(`SELECT predecessor_owner_id FROM owner_succession_authorizations WHERE authorization_id=?`, p.AuthorizationID).Scan(&r.predecessor); err != nil {
		t.Fatal(err)
	}
	if err = f.db.sql.QueryRow(`SELECT original_binding_hash FROM execution_owners WHERE owner_id=?`, r.owner).Scan(&r.binding); err != nil {
		t.Fatal(err)
	}
	if err = f.db.sql.QueryRow(`SELECT original_binding_hash,control_revision FROM execution_owners WHERE owner_id=?`, r.predecessor).Scan(&r.priorBinding, &r.predecessorControl); err != nil {
		t.Fatal(err)
	}
	return f, r
}

func insertRecoveryHandoffSchema(db *sql.DB, r recoveryHandoffSchemaRow) error {
	_, err := db.Exec(`INSERT INTO owner_recovery_scope_handoffs(owner_id,authorization_id,run_id,launch_id,producer_id,predecessor_owner_id,prior_scope_hash,scope_hash,prior_record_hash,record_hash,authority_hash,created_at,coordinator_epoch,prior_binding_hash,binding_hash,predecessor_control_revision,control_revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1)`, r.owner, r.authorization, r.run, r.launch, r.producer, r.predecessor, r.priorScope, r.scope, r.priorRecord, r.record, r.authority, r.created, r.epoch, r.priorBinding, r.binding, r.predecessorControl)
	return err
}
func insertRecoveryAppliedSchema(db *sql.DB, r recoveryHandoffSchemaRow) error {
	result, err := db.Exec(`UPDATE owner_recovery_scope_handoffs SET applied=1 WHERE owner_id=? AND run_id=? AND record_hash=? AND coordinator_epoch<=? AND ?=(SELECT epoch FROM coordinator WHERE id=1)`, r.owner, r.run, r.record, r.epoch, r.epoch)
	return ownerOneRow(result, err)
}

func TestOwnerRecoveryHandoffSchemaAccountsAndFreezes(t *testing.T) {
	f, r := recoveryHandoffSchemaFixture(t)
	before := collectionUsage(t, f.db.sql, "global", "")
	beforeRun := collectionUsage(t, f.db.sql, "run", r.run)
	if err := insertRecoveryHandoffSchema(f.db.sql, r); err != nil {
		t.Fatal("missing adjacent handoff table", err)
	}
	want := int64(32)
	for _, v := range []string{r.owner, r.authorization, r.run, r.launch, r.producer, r.predecessor, r.priorScope, r.scope, r.priorRecord, r.record, r.authority, r.priorBinding, r.binding, r.created} {
		want += int64(len(v))
	}
	if delta := collectionUsage(t, f.db.sql, "global", "") - before; delta != want {
		t.Fatalf("intent charge %d want %d", delta, want)
	}
	if delta := collectionUsage(t, f.db.sql, "run", r.run) - beforeRun; delta != want {
		t.Fatal("run charge", delta, want)
	}
	if err := insertRecoveryAppliedSchema(f.db.sql, r); err != nil {
		t.Fatal(err)
	}
	// Applying consumes the precharged fixed-width slot; no growth.
	if delta := collectionUsage(t, f.db.sql, "global", "") - before; delta != want {
		t.Fatalf("applied charge %d want %d", delta, want)
	}
	for _, table := range []string{"owner_recovery_scope_handoffs"} {
		for _, query := range []string{"UPDATE " + table + " SET created_at=created_at", "DELETE FROM " + table} {
			t.Run(query, func(t *testing.T) {
				if _, err := f.db.sql.Exec(query); err == nil {
					t.Fatal("immutable receipt changed")
				}
			})
		}
	}
	if err := insertRecoveryHandoffSchema(f.db.sql, r); err == nil {
		t.Fatal("duplicate intent")
	}
	if err := insertRecoveryAppliedSchema(f.db.sql, r); err == nil {
		t.Fatal("duplicate applied")
	}
	if delta := collectionUsage(t, f.db.sql, "global", "") - before; delta != want {
		t.Fatal("rejection changed quota")
	}
}

func TestOwnerRecoveryHandoffSchemaRejectsUnboundProofs(t *testing.T) {
	changes := map[string]func(*recoveryHandoffSchemaRow){
		"owner":         func(r *recoveryHandoffSchemaRow) { r.owner = r.predecessor },
		"authorization": func(r *recoveryHandoffSchemaRow) { r.authorization = "foreign" },
		"predecessor":   func(r *recoveryHandoffSchemaRow) { r.predecessor = r.owner },
		"run":           func(r *recoveryHandoffSchemaRow) { r.run = "foreign" },
		"launch":        func(r *recoveryHandoffSchemaRow) { r.launch = "foreign" },
		"producer":      func(r *recoveryHandoffSchemaRow) { r.producer = "foreign" },
		"epoch":         func(r *recoveryHandoffSchemaRow) { r.epoch++ },
		"uppercase":     func(r *recoveryHandoffSchemaRow) { r.record = strings.Repeat("A", 64) },
		"bad_digest":    func(r *recoveryHandoffSchemaRow) { r.priorScope = strings.Repeat("x", 64) },
		"same_scope":    func(r *recoveryHandoffSchemaRow) { r.scope = r.priorScope },
		"same_record":   func(r *recoveryHandoffSchemaRow) { r.record = r.priorRecord },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			f, r := recoveryHandoffSchemaFixture(t)
			before := ownerSuccessionResumeSnapshot(t, f.db)
			change(&r)
			if err := insertRecoveryHandoffSchema(f.db.sql, r); err == nil {
				t.Fatal("invalid proof accepted")
			}
			if ownerSuccessionResumeSnapshot(t, f.db) != before {
				t.Fatal("rejection changed state")
			}
		})
	}
}

func TestOwnerRecoveryHandoffAppliedRequiresExactIntent(t *testing.T) {
	for _, name := range []string{"missing", "record", "owner", "run", "epoch"} {
		t.Run(name, func(t *testing.T) {
			f, r := recoveryHandoffSchemaFixture(t)
			if name != "missing" {
				if err := insertRecoveryHandoffSchema(f.db.sql, r); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "record":
				r.record = strings.Repeat("f", 64)
			case "owner":
				r.owner = r.predecessor
			case "run":
				r.run = "foreign"
			case "epoch":
				r.epoch++
			}
			before := collectionUsage(t, f.db.sql, "global", "")
			if err := insertRecoveryAppliedSchema(f.db.sql, r); err == nil {
				t.Fatal("unproven application accepted")
			}
			if collectionUsage(t, f.db.sql, "global", "") != before {
				t.Fatal("rejection charged quota")
			}
		})
	}
}

func TestOwnerRecoveryHandoffSchemaAllowsCoordinatorRestartBeforePrepareOrApply(t *testing.T) {
	for _, stage := range []string{"before_prepare", "before_apply"} {
		t.Run(stage, func(t *testing.T) {
			f, r := recoveryHandoffSchemaFixture(t)
			if stage == "before_apply" {
				if err := insertRecoveryHandoffSchema(f.db.sql, r); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.db.BeginCoordinatorEpoch(context.Background(), 2); err != nil {
				t.Fatal(err)
			}
			r.epoch = 2
			if stage == "before_prepare" {
				if err := insertRecoveryHandoffSchema(f.db.sql, r); err != nil {
					t.Fatal("same bound owner cannot prepare after restart", err)
				}
			}
			if err := insertRecoveryAppliedSchema(f.db.sql, r); err != nil {
				t.Fatal("same bound owner cannot apply after restart", err)
			}
		})
	}
}

func TestOwnerRecoveryHandoffSchemaCannotChangeProofWhileApplying(t *testing.T) {
	for _, column := range []string{"owner_id", "authorization_id", "run_id", "launch_id", "producer_id", "predecessor_owner_id", "prior_scope_hash", "scope_hash", "prior_record_hash", "record_hash", "authority_hash", "prior_binding_hash", "binding_hash", "created_at", "predecessor_control_revision", "control_revision", "coordinator_epoch"} {
		t.Run(column, func(t *testing.T) {
			f, r := recoveryHandoffSchemaFixture(t)
			if err := insertRecoveryHandoffSchema(f.db.sql, r); err != nil {
				t.Fatal(err)
			}
			before := ownerSuccessionResumeTablesSnapshot(t, f.db, "owner_recovery_scope_handoffs", "storage_usage")
			value := column + " || 'x'"
			if column == "predecessor_control_revision" || column == "control_revision" || column == "coordinator_epoch" {
				value = column + "+1"
			}
			if _, err := f.db.sql.Exec("UPDATE owner_recovery_scope_handoffs SET applied=1," + column + "=" + value); err == nil {
				t.Fatal("application rewrote proof", column)
			}
			if before != ownerSuccessionResumeTablesSnapshot(t, f.db, "owner_recovery_scope_handoffs", "storage_usage") {
				t.Fatal("rejection changed proof/accounting")
			}
		})
	}
}
