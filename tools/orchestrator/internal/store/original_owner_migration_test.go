package store

import (
	"context"
	"database/sql"
	"testing"
)

func originalOwnerSchemaCount(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOriginalOwnerV11MigrationPreservesLegacyRowsAndReopens(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	historicalOwnerClaim(t, f)
	var before string
	if err := f.db.sql.QueryRow(`SELECT group_concat(id||':'||pid||':'||birth||':'||coordinator_epoch) FROM runtime_hosts`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Remove every additive v12 object, rather than only backdating schema_meta.
	downgradePolicySchema(t, f.db)
	if _, err := f.db.sql.Exec(`DROP TABLE config_preparation_abandons; DROP TABLE decision_attempt_bindings; DROP TABLE owner_control_changes; DROP TABLE execution_owners; ALTER TABLE host_launches DROP COLUMN execution_owner_mode; UPDATE schema_meta SET version=11`); err != nil {
		t.Fatal(err)
	}
	if originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM pragma_table_info('host_launches') WHERE name='execution_owner_mode'`) != 0 || originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%execution_owners%' OR name LIKE '%owner_control_changes%'`) != 0 {
		t.Fatal("fixture retains v12 DDL")
	}
	path := f.db.path
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if originalOwnerSchemaCount(t, db.sql, `SELECT version FROM schema_meta`) != currentSchemaVersion || originalOwnerSchemaCount(t, db.sql, `SELECT COUNT(*) FROM host_launches WHERE execution_owner_mode!='legacy'`) != 0 || originalOwnerSchemaCount(t, db.sql, `SELECT COUNT(*) FROM execution_owners`) != 0 {
		t.Fatal("migration changed legacy mode or enrolled a historical execution")
	}
	var after string
	if err := db.sql.QueryRow(`SELECT group_concat(id||':'||pid||':'||birth||':'||coordinator_epoch) FROM runtime_hosts`).Scan(&after); err != nil || before != after {
		t.Fatal("migration changed original Host identities", err)
	}
	if originalOwnerSchemaCount(t, db.sql, `SELECT COUNT(*) FROM segment_runtime`) != 1 || originalOwnerSchemaCount(t, db.sql, `SELECT COUNT(*) FROM attempts`) != 1 || originalOwnerSchemaCount(t, db.sql, `SELECT COUNT(*) FROM owner_control_changes`) != 0 {
		t.Fatal("migration rewrote original business execution")
	}
	var used int64
	if err := db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var reopenedUsage int64
	if err := reopened.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&reopenedUsage); err != nil || used != reopenedUsage {
		t.Fatal("reopen repeated migration/backfill", err)
	}
	if originalOwnerSchemaCount(t, reopened.sql, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND (name LIKE 'quota_execution_owners_%' OR name LIKE 'quota_owner_control_changes_%')`) != 6 {
		t.Fatal("owner records not charged by all insert/update/delete triggers")
	}
}

func TestOriginalOwnerV11MigrationFailureRollsBackDDLAndVersion(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	downgradePolicySchema(t, f.db)
	if _, err := f.db.sql.Exec(`DROP TABLE config_preparation_abandons; DROP TABLE decision_attempt_bindings; DROP TABLE owner_control_changes; DROP TABLE execution_owners; ALTER TABLE host_launches DROP COLUMN execution_owner_mode; UPDATE schema_meta SET version=11; CREATE TABLE owner_control_changes(injected TEXT)`); err != nil {
		t.Fatal(err)
	}
	path := f.db.path
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(path); err == nil {
		opened.Close()
		t.Fatal("malformed prior schema accepted")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if originalOwnerSchemaCount(t, raw, `SELECT version FROM schema_meta`) != 11 || originalOwnerSchemaCount(t, raw, `SELECT COUNT(*) FROM pragma_table_info('host_launches') WHERE name='execution_owner_mode'`) != 0 || originalOwnerSchemaCount(t, raw, `SELECT COUNT(*) FROM sqlite_master WHERE name='execution_owners'`) != 0 {
		t.Fatal("failed migration partially committed DDL/version")
	}
	if originalOwnerSchemaCount(t, raw, `SELECT COUNT(*) FROM runs WHERE id='owner-run'`) != 1 {
		t.Fatal("failed migration removed original run")
	}
}

func TestOriginalOwnerCurrentSlotCoversIntendedExpiryAndGeneration(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	ctx := context.Background()
	f.prepare.StartupDeadlineUnixMS = 1 // Audit expiry never proves owner death.
	i, err := f.db.PrepareOriginalOwner(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	f.db.BeginCoordinatorEpoch(ctx, 2)
	f.db.RegisterHost(ctx, f.prepare.Host, 2)
	next := f.prepare
	next.Epoch, next.RequestID, next.BootstrapID = 2, "another-intent", "another-bootstrap"
	if _, err := f.db.PrepareOriginalOwner(ctx, next); err == nil {
		t.Fatal("elapsed startup deadline or new epoch freed intended current slot")
	}
	state, err := f.db.InspectOriginalOwner(ctx, i.Scope.OwnerID)
	if err != nil || !state.IsCurrent || state.Intent != i || state.ControlRevision != 0 || state.CurrentHost != nil || state.Binding != nil {
		t.Fatal("uncertain intended owner gained a lease or was overwritten", err)
	}
}

func TestOriginalOwnerSchemaRetainsHistoricalOwnerAndSingleCurrentAcrossGenerations(t *testing.T) {
	f, i, _ := originalOwnerBound(t)
	next := i
	next.Scope.OwnerID, next.Scope.Incarnation, next.Scope.HostGeneration = "future-owner", 2, "next-generation"
	next.BootstrapID, next.RequestID = "future-bootstrap", "future-request"
	if err := insertOwnerSchemaSuccessor(f.db, next, nil); err == nil {
		t.Fatal("another generation bypassed launch-wide current-owner uniqueness")
	}
	// This synthetic schema fixture supplies the required immutable authorization;
	// it is not evidence of a real process exit or the public succession API.
	authorization := insertOwnerSuccessionSchemaAuthorization(t, f.db, i, next)
	if _, err := f.db.sql.Exec(`UPDATE execution_owners SET phase='retired',is_current=0 WHERE owner_id=?`, i.Scope.OwnerID); err != nil {
		t.Fatal(err)
	}
	if err := insertOwnerSchemaSuccessor(f.db, next, authorization); err != nil {
		t.Fatal("schema permanently forbids legal later owner incarnation", err)
	}
	old, err := f.db.InspectOriginalOwner(context.Background(), i.Scope.OwnerID)
	if err != nil || old.IsCurrent || old.Phase != "retired" || old.Intent != i || old.Binding == nil {
		t.Fatal("historical immutable owner was lost", err)
	}
	if originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM execution_owners`) != 2 || originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM execution_owners WHERE is_current=1`) != 1 || originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM owner_control_changes`) != 1 {
		t.Fatal("history/current slot or original permit changed")
	}
	duplicate := next
	duplicate.Scope.OwnerID, duplicate.BootstrapID, duplicate.RequestID = "duplicate-incarnation", "duplicate-bootstrap", "duplicate-request"
	if err := insertOwnerSchemaSuccessor(f.db, duplicate, authorization); err == nil {
		t.Fatal("caller could reuse an existing launch incarnation", err)
	}
	if originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM execution_owners`) != 2 {
		t.Fatal("rejected duplicate mutated owner history")
	}
}
