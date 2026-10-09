package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// A historical fixture may remove empty additive DDL, never launch evidence.
func removeOwnerLaunchSchema32(db *DB) error {
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version, n int
	if err = tx.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil {
		return err
	}
	if version < 32 {
		return nil
	}
	if version != 32 {
		return fmt.Errorf("unsupported launch schema %d", version)
	}
	if err = tx.QueryRow(`SELECT (SELECT COUNT(*) FROM owner_launch_attempts)+(SELECT COUNT(*) FROM owner_launch_heads)+(SELECT COUNT(*) FROM owner_launch_receipts)+(SELECT COUNT(*) FROM owner_launch_escrows)`).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return errors.New("cannot erase original owner launch history")
	}
	if _, err = tx.Exec(`DROP TABLE owner_launch_heads; DROP TABLE owner_launch_receipts; DROP TABLE owner_launch_escrows; DROP TABLE owner_launch_attempts; UPDATE schema_meta SET version=31`); err != nil {
		return err
	}
	return tx.Commit()
}

func TestOwnerLaunchMigrationPreservesLegacyAndRefusesProofErasure(t *testing.T) {
	ctx := context.Background()
	f := newOriginalOwnerFixture(t)
	intent, err := f.db.PrepareOriginalOwner(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.db.InspectOriginalOwner(ctx, intent.Scope.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	usage := ownerLaunchUsage(t, f)
	if err = removeOwnerLaunchSchema32(f.db); err != nil {
		t.Fatal(err)
	}
	if err = f.db.migrateRuntime(); err != nil {
		t.Fatal(err)
	}
	after, err := f.db.InspectOriginalOwner(ctx, intent.Scope.OwnerID)
	if err != nil || !reflect.DeepEqual(before, after) || ownerLaunchUsage(t, f) != usage {
		t.Fatal("schema32 changed legacy owner", err)
	}
	if _, err = f.db.InspectOriginalOwnerLaunch(ctx, intent.Scope.OwnerID); !errors.Is(err, ErrNotFound) {
		t.Fatal("migration invented v2 proof", err)
	}
	if _, err = f.db.PrepareSupervisedOriginalOwner(ctx, f.prepare, "", ownerLaunchSupervisor(f), f.key); err == nil {
		t.Fatal("existing v1 owner upgraded to v2")
	}
	v2 := newOriginalOwnerFixture(t)
	if _, err = v2.db.PrepareSupervisedOriginalOwner(ctx, v2.prepare, "", ownerLaunchSupervisor(v2), v2.key); err != nil {
		t.Fatal(err)
	}
	beforeUsage := ownerLaunchUsage(t, v2)
	if err = removeOwnerLaunchSchema32(v2.db); err == nil || ownerLaunchUsage(t, v2) != beforeUsage {
		t.Fatal("historical fixture erased launch proof", err)
	}
}
