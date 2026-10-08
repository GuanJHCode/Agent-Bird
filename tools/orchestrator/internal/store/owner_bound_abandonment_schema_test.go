package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"strings"
	"testing"
)

func TestOwnerBoundAbandonmentSchemaStartsWithoutInventedProof(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	var version, proofs, references int
	if err := f.db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 28 {
		t.Fatalf("bound abandonment schema version=%d want 28", version)
	}
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM owner_bound_abandonments`).Scan(&proofs); err != nil || proofs != 0 {
		t.Fatal("fresh schema invented abandonment proof", proofs, err)
	}
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM owner_succession_authorizations WHERE abandoned_owner_id IS NOT NULL`).Scan(&references); err != nil || references != 0 {
		t.Fatal("fresh schema invented abandonment authority", references, err)
	}
}

// These are trusted Store/schema facts, not kernel death or filesystem evidence.
const abandonmentSchemaTestColumns = `owner_id,inbound_authorization_id,run_id,task_id,launch_id,producer_id,binding_hash,scope_hash,permit_hash,owner_birth,owner_exit_proof,owner_exited_at,host_birth,host_exit_proof,host_exited_at,origin_context_id,origin_birth,host_generation,executable,executable_sha256,last_segment_id,created_at,control_revision,owner_pid,host_pid,origin_pid,coordinator_epoch,work_revision,ready_sequence,segment_count`

func abandonmentSchemaFixture(t *testing.T) (originalOwnerFixture, OwnerRecoveryHandoffSpec, map[string]any) {
	t.Helper()
	f, spec, facts, _ := recoveryHandoffAPIFixture(t)
	if _, err := f.db.PrepareOriginalOwnerRecoveryHandoff(context.Background(), spec, facts, f.key.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
	columns := strings.Split(abandonmentSchemaTestColumns, ",")
	values := make([]any, len(columns))
	ptrs := make([]any, len(values))
	for i := range ptrs {
		ptrs[i] = &values[i]
	}
	err := f.db.sql.QueryRow(`SELECT n.owner_id,n.succession_authorization_id,n.run_id,a.task_id,n.launch_id,n.producer_id,n.original_binding_hash,h.scope_hash,c.permit_hash,n.owner_birth,'identity_exited_v1','2026-10-08T01:00:00Z',n.current_host_birth,'identity_exited_v1','2026-10-08T01:00:00Z',r.origin_context_id,r.origin_birth,l.host_generation,l.executable,a.successor_executable_sha256,COALESCE((SELECT s.id FROM segments s JOIN attempts x ON x.id=s.attempt_id WHERE x.task_id=a.task_id ORDER BY x.attempt_no DESC,s.segment_no DESC LIMIT 1),''),'2026-10-08T01:00:00Z',n.control_revision,n.owner_pid,n.current_host_pid,r.origin_pid,(SELECT epoch FROM coordinator WHERE id=1),a.work_revision,a.ready_sequence,(SELECT COUNT(*) FROM segments s JOIN attempts x ON x.id=s.attempt_id WHERE x.task_id=a.task_id)
 FROM execution_owners n JOIN owner_succession_authorizations a ON a.authorization_id=n.succession_authorization_id JOIN owner_recovery_scope_handoffs h ON h.owner_id=n.owner_id JOIN owner_control_changes c ON c.owner_id=n.owner_id AND c.revision=n.control_revision JOIN runs r ON r.id=n.run_id JOIN host_launches l ON l.id=n.launch_id WHERE n.owner_id=?`, spec.OwnerID).Scan(ptrs...)
	if err != nil {
		t.Fatal(err)
	}
	row := map[string]any{}
	for i, c := range columns {
		row[c] = values[i]
	}
	return f, spec, row
}

type abandonmentSchemaInserter interface {
	Exec(string, ...any) (sql.Result, error)
}

func insertAbandonmentSchemaRow(db abandonmentSchemaInserter, row map[string]any) error {
	columns := strings.Split(abandonmentSchemaTestColumns, ",")
	values := make([]any, len(columns))
	for i, c := range columns {
		values[i] = row[c]
	}
	_, err := db.Exec(`INSERT INTO owner_bound_abandonments(`+abandonmentSchemaTestColumns+`) VALUES(`+strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",")+`)`, values...)
	return err
}

func TestOwnerBoundAbandonmentSchemaAccountsFreezesAndSeals(t *testing.T) {
	f, spec, row := abandonmentSchemaFixture(t)
	before := collectionUsage(t, f.db.sql, "global", "")
	beforeRun := collectionUsage(t, f.db.sql, "run", row["run_id"].(string))
	if err := insertAbandonmentSchemaRow(f.db.sql, row); err != nil {
		t.Fatal("missing durable death proof", err)
	}
	// Eight fixed-width integers; all 22 text fields are UTF-8 byte charged.
	want := int64(64)
	for _, v := range row {
		if text, ok := v.(string); ok {
			want += int64(len(text))
		}
	}
	if got := collectionUsage(t, f.db.sql, "global", "") - before; got != want {
		t.Fatal("proof global charge", got, want)
	}
	if got := collectionUsage(t, f.db.sql, "run", row["run_id"].(string)) - beforeRun; got != want {
		t.Fatal("proof run charge", got, want)
	}
	for _, query := range []string{`UPDATE owner_bound_abandonments SET created_at=created_at`, `DELETE FROM owner_bound_abandonments`} {
		if _, err := f.db.sql.Exec(query); err == nil || !strings.Contains(err.Error(), "owner_bound_abandonment_immutable") {
			t.Fatal("proof mutable", err)
		}
	}
	for _, query := range []string{
		`UPDATE owner_control_changes SET applied_hash='` + strings.Repeat("a", 64) + `' WHERE owner_id=?`,
		`UPDATE execution_owners SET applied_control_revision=control_revision WHERE owner_id=?`,
		`UPDATE execution_owners SET coordinator_epoch=coordinator_epoch+1 WHERE owner_id=?`,
	} {
		if _, err := f.db.sql.Exec(query, spec.OwnerID); err == nil || !strings.Contains(err.Error(), "owner_bound_abandonment_sealed") {
			t.Fatal("dead owner regained mutable control", err)
		}
	}
	if err := insertAbandonmentSchemaRow(f.db.sql, row); err == nil {
		t.Fatal("duplicate death proof")
	}
	if got := collectionUsage(t, f.db.sql, "global", "") - before; got != want {
		t.Fatal("rejection changed charge", got, want)
	}
}

func TestOwnerBoundAbandonmentSchemaRejectsDrift(t *testing.T) {
	for _, column := range []string{"inbound_authorization_id", "run_id", "task_id", "launch_id", "producer_id", "binding_hash", "scope_hash", "permit_hash", "owner_birth", "host_birth", "origin_birth", "host_generation", "executable_sha256", "last_segment_id", "control_revision", "owner_pid", "host_pid", "coordinator_epoch", "work_revision", "ready_sequence", "segment_count"} {
		t.Run(column, func(t *testing.T) {
			f, _, row := abandonmentSchemaFixture(t)
			// A valid insert must work in this same fixture before testing one drift.
			tx, err := f.db.sql.Begin()
			if err != nil {
				t.Fatal(err)
			}
			err = insertAbandonmentSchemaRow(tx, row)
			rollbackErr := tx.Rollback()
			if err != nil || rollbackErr != nil {
				t.Fatal("invalid baseline", err, rollbackErr)
			}
			before := ownerSuccessionResumeTablesSnapshot(t, f.db, "owner_bound_abandonments", "storage_usage", "execution_owners", "owner_control_changes")
			switch v := row[column].(type) {
			case string:
				row[column] = v + "x"
			case int64:
				row[column] = v + 1
			default:
				t.Fatalf("unexpected fixture type %s %T", column, v)
			}
			if err = insertAbandonmentSchemaRow(f.db.sql, row); err == nil {
				t.Fatal("unproved abandonment accepted", column)
			}
			if before != ownerSuccessionResumeTablesSnapshot(t, f.db, "owner_bound_abandonments", "storage_usage", "execution_owners", "owner_control_changes") {
				t.Fatal("rejection changed durable state")
			}
		})
	}
}
