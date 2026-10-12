package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestOwnerSuccessionSchemaRejectsUnbackedRetirement(t *testing.T) {
	f, intent, _ := originalOwnerBound(t)
	if _, err := f.db.sql.Exec(`UPDATE execution_owners SET phase='retired',is_current=0 WHERE owner_id=?`, intent.Scope.OwnerID); err == nil {
		t.Fatal("bound owner retired without immutable succession authorization")
	}
	state, err := f.db.InspectOriginalOwner(context.Background(), intent.Scope.OwnerID)
	if err != nil || state.Phase != "bound" || !state.IsCurrent {
		t.Fatal("rejected retirement changed the original current owner", err)
	}
}

func TestOwnerSuccessionSchemaFreezesControlProof(t *testing.T) {
	for _, change := range []string{"scope_hash='" + strings.Repeat("b", 64) + "'", "coordinator_epoch=coordinator_epoch+1", "permit_json=json_set(permit_json,'$.permit_id','foreign')", "request_id='foreign'", "DELETE"} {
		t.Run(change, func(t *testing.T) {
			f, i, _ := originalOwnerBound(t)
			before := originalOwnerSnapshot(t, f)
			used := originalOwnerUsed(t, f)
			query := "UPDATE owner_control_changes SET " + change + " WHERE owner_id=?"
			if change == "DELETE" {
				query = "DELETE FROM owner_control_changes WHERE owner_id=?"
			}
			if _, err := f.db.sql.Exec(query, i.Scope.OwnerID); err == nil {
				t.Fatal("control proof was rewritten or deleted before succession")
			}
			if originalOwnerSnapshot(t, f) != before || originalOwnerUsed(t, f) != used {
				t.Fatal("rejected control proof mutation changed accounting or history")
			}
		})
	}
}

func TestOwnerSuccessionSchemaAcceptsCurrentEpochAfterOldOwnerExit(t *testing.T) {
	f, i, _ := originalOwnerBound(t)
	if err := f.db.BeginCoordinatorEpoch(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	next := i
	next.Scope.OwnerID, next.Scope.Incarnation = "successor", 2
	next.CoordinatorEpoch = 2
	auth := insertOwnerSuccessionSchemaAuthorization(t, f.db, i, next)
	var epoch, ownerEpoch, permitEpoch uint64
	if err := f.db.sql.QueryRow(`SELECT a.coordinator_epoch,p.coordinator_epoch,c.coordinator_epoch FROM owner_succession_authorizations a JOIN execution_owners p ON p.owner_id=a.predecessor_owner_id JOIN owner_control_changes c ON c.owner_id=p.owner_id AND c.revision=p.control_revision WHERE a.authorization_id=?`, auth).Scan(&epoch, &ownerEpoch, &permitEpoch); err != nil || epoch != 2 || ownerEpoch != 1 || permitEpoch != 1 {
		t.Fatalf("new coordinator epoch changed predecessor history: %d/%d/%d: %v", epoch, ownerEpoch, permitEpoch, err)
	}
}

func TestOwnerSuccessionSchemaFreezesBoundIdentity(t *testing.T) {
	for _, change := range []string{"owner_pid=owner_pid+1", "owner_birth='foreign'", "owner_public_key='" + strings.Repeat("b", 64) + "'", "original_binding_json=json_set(original_binding_json,'$.owner.birth','foreign')", "original_binding_hash='" + strings.Repeat("b", 64) + "'"} {
		t.Run(change, func(t *testing.T) {
			f, i, _ := originalOwnerBound(t)
			before, used := originalOwnerSnapshot(t, f), originalOwnerUsed(t, f)
			if _, err := f.db.sql.Exec("UPDATE execution_owners SET "+change+" WHERE owner_id=?", i.Scope.OwnerID); err == nil {
				t.Fatal("bound original identity changed")
			}
			if before != originalOwnerSnapshot(t, f) || used != originalOwnerUsed(t, f) {
				t.Fatal("rejected bound identity mutation changed history or quota")
			}
		})
	}
}

// Synthetic schema fixture only. Coordinator's positive process observation
// and Store's quiescent authenticated transaction require separate API tests.
func insertOwnerSuccessionSchemaAuthorization(t *testing.T, db *DB, prior, next contract.OriginalOwnerIntent) string {
	t.Helper()
	prepareOwnerSuccessionSchemaTask(t, db, prior.Scope.LaunchID)
	id, err := tryOwnerSuccessionSchemaAuthorization(db, prior, next, "")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func prepareOwnerSuccessionSchemaTask(t *testing.T, db *DB, launchID string) {
	t.Helper()
	// Synthetic QueueResume state for DDL tests, not a business authorization.
	if _, err := db.sql.Exec(`UPDATE tasks SET status='resume_queued' WHERE id=(SELECT task_id FROM task_runtime WHERE host_launch_id=? ORDER BY task_id LIMIT 1)`, launchID); err != nil {
		t.Fatal(err)
	}
}

func tryOwnerSuccessionSchemaAuthorization(db *DB, prior, next contract.OriginalOwnerIntent, drift string) (string, error) {
	hash, err := contract.OriginalOwnerScopeSHA256(prior.Scope)
	if err != nil {
		return "", err
	}
	var epoch, sequence int64
	if err = db.sql.QueryRow(`SELECT (SELECT epoch FROM coordinator WHERE id=1),ready_sequence FROM task_runtime WHERE host_launch_id=? ORDER BY task_id LIMIT 1`, prior.Scope.LaunchID).Scan(&epoch, &sequence); err != nil {
		return "", err
	}
	switch drift {
	case "scope":
		hash = strings.Repeat("b", 64)
	case "epoch":
		epoch++
	case "sequence":
		sequence++
	}
	id := "succession-auth-界"
	r, err := db.sql.Exec(`INSERT INTO owner_succession_authorizations(authorization_id,command_id,request_hash,run_id,task_id,work_revision,launch_id,predecessor_owner_id,predecessor_incarnation,predecessor_control_revision,predecessor_scope_hash,owner_pid,owner_birth,owner_exit_proof,owner_exited_at,host_pid,host_birth,host_exit_proof,host_exited_at,successor_origin_context_id,successor_origin_pid,successor_origin_birth,successor_host_generation,successor_executable,successor_executable_sha256,coordinator_epoch,ready_sequence,created_at)
 SELECT ?,?, ?,p.run_id,tr.task_id,tr.work_revision,p.launch_id,p.owner_id,p.incarnation,p.control_revision,?,p.owner_pid,p.owner_birth,'identity_exited_v1','fixture-observation',p.current_host_pid,p.current_host_birth,'identity_exited_v1','fixture-observation',?,?,?,?,?,?,?,?,'fixture-created'
 FROM execution_owners p JOIN task_runtime tr ON tr.host_launch_id=p.launch_id WHERE p.owner_id=? ORDER BY tr.task_id LIMIT 1`, id, "resume-fixture-command", strings.Repeat("a", 64), hash, next.Scope.OriginalOriginContextID, next.Scope.OriginalOriginPID, next.Scope.OriginalOriginBirth, next.Scope.HostGeneration, next.Scope.Executable, next.Scope.ExecutableSHA256, epoch, sequence, prior.Scope.OwnerID)
	if err != nil {
		return "", err
	}
	n, err := r.RowsAffected()
	if err != nil || n != 1 {
		return "", fmt.Errorf("missing schema authorization fixture: %w", err)
	}
	return id, nil
}

func TestOwnerSuccessionSchemaRejectsDriftedAuthorizationFacts(t *testing.T) {
	for _, drift := range []string{"scope", "epoch", "sequence"} {
		t.Run(drift, func(t *testing.T) {
			f, i, _ := originalOwnerBound(t)
			next := i
			next.Scope.OwnerID = "next"
			next.Scope.Incarnation = 2
			prepareOwnerSuccessionSchemaTask(t, f.db, i.Scope.LaunchID)
			before := collectionUsage(t, f.db.sql, "global", "")
			if _, err := tryOwnerSuccessionSchemaAuthorization(f.db, i, next, drift); err == nil {
				t.Fatal("authorization accepted a fact absent from durable owner/task state")
			}
			if originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM owner_succession_authorizations`) != 0 || collectionUsage(t, f.db.sql, "global", "") != before {
				t.Fatal("rejected authorization changed accounting")
			}
		})
	}
}

func TestOwnerSuccessionSchemaNeverDeletesAnUnprovedOwner(t *testing.T) {
	for _, successor := range []bool{false, true} {
		t.Run(fmt.Sprint(successor), func(t *testing.T) {
			f := newOriginalOwnerFixture(t)
			i, err := f.db.PrepareOriginalOwner(context.Background(), f.prepare)
			if err != nil {
				t.Fatal(err)
			}
			id := i.Scope.OwnerID
			if successor {
				if _, err = f.db.BindOriginalOwner(context.Background(), f.bindSpec(i), f.key); err != nil {
					t.Fatal(err)
				}
				next := i
				next.Scope.OwnerID = "next"
				next.Scope.Incarnation = 2
				next.BootstrapID = "next-bootstrap"
				next.RequestID = "next-prepare"
				auth := insertOwnerSuccessionSchemaAuthorization(t, f.db, i, next)
				if _, err = f.db.sql.Exec(`UPDATE execution_owners SET phase='retired',is_current=0 WHERE owner_id=?`, id); err != nil {
					t.Fatal(err)
				}
				if err = insertOwnerSchemaSuccessor(f.db, next, auth); err != nil {
					t.Fatal(err)
				}
				id = next.Scope.OwnerID
			}
			if _, err = f.db.sql.Exec(`DELETE FROM execution_owners WHERE owner_id=?`, id); err == nil {
				t.Fatal("DELETE discarded unproved original identity and freed incarnation/authorization")
			}
		})
	}
}

func TestOwnerSuccessionSchemaFreezesOwnerIdentityAndRetiredHistory(t *testing.T) {
	for _, change := range []string{"incarnation=incarnation+1", "intent_json=json_set(intent_json,'$.scope.host_generation','foreign')", "bootstrap_id='foreign'", "request_id='foreign'", "created_at='foreign'", "phase='bound'"} {
		t.Run(change, func(t *testing.T) {
			f, i, _ := originalOwnerBound(t)
			next := i
			next.Scope.OwnerID = "next"
			next.Scope.Incarnation = 2
			next.BootstrapID = "next-bootstrap"
			next.RequestID = "next-prepare"
			auth := insertOwnerSuccessionSchemaAuthorization(t, f.db, i, next)
			if _, err := f.db.sql.Exec(`UPDATE execution_owners SET phase='retired',is_current=0 WHERE owner_id=?`, i.Scope.OwnerID); err != nil {
				t.Fatal(err)
			}
			if err := insertOwnerSchemaSuccessor(f.db, next, auth); err != nil {
				t.Fatal(err)
			}
			id := next.Scope.OwnerID
			if change == "phase='bound'" {
				id = i.Scope.OwnerID
			}
			if _, err := f.db.sql.Exec("UPDATE execution_owners SET "+change+" WHERE owner_id=?", id); err == nil {
				t.Fatal("immutable authorized identity or retired history changed")
			}
		})
	}
}

func TestOwnerSuccessionSchemaRejectsUnknownScopeVersion(t *testing.T) {
	f, i, _ := originalOwnerBound(t)
	next := i
	next.Scope.OwnerID = "next"
	next.Scope.Incarnation = 2
	next.BootstrapID = "next-bootstrap"
	next.RequestID = "next-prepare"
	auth := insertOwnerSuccessionSchemaAuthorization(t, f.db, i, next)
	if _, err := f.db.sql.Exec(`UPDATE execution_owners SET phase='retired',is_current=0 WHERE owner_id=?`, i.Scope.OwnerID); err != nil {
		t.Fatal(err)
	}
	next.Scope.Version = 2
	if err := insertOwnerSchemaSuccessor(f.db, next, auth); err == nil {
		t.Fatal("authorization interpreted an unknown owner scope version")
	}
}

func insertOwnerSchemaSuccessor(db *DB, next contract.OriginalOwnerIntent, authorization any) error {
	body, err := json.Marshal(next)
	if err != nil {
		return err
	}
	_, err = db.sql.Exec(`INSERT INTO execution_owners(owner_id,launch_id,run_id,producer_id,incarnation,bootstrap_id,bootstrap_token_hash,coordinator_key_id,request_id,request_hash,intent_json,phase,is_current,control_revision,coordinator_epoch,created_at,updated_at,succession_authorization_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,'intended',1,0,?,'now','now',?)`, next.Scope.OwnerID, next.Scope.LaunchID, next.Scope.RunID, next.Scope.ProducerID, next.Scope.Incarnation, next.BootstrapID, next.BootstrapTokenSHA256, next.CoordinatorKeyID, next.RequestID, next.RequestSHA256, string(body), next.CoordinatorEpoch, authorization)
	return err
}

func downgradeOwnerSuccessionSchema25(t *testing.T, db *DB) {
	t.Helper()
	downgradeExistingOwnerResumeSchema26(t, db)
	var version int
	if err := db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version <= 24 {
		return
	}
	if version != 25 {
		t.Fatalf("cannot remove owner succession schema from version %d", version)
	}
	if originalOwnerSchemaCount(t, db.sql, `SELECT COUNT(*) FROM owner_succession_authorizations`)+originalOwnerSchemaCount(t, db.sql, `SELECT COUNT(*) FROM execution_owners WHERE succession_authorization_id IS NOT NULL`) != 0 {
		t.Fatal("downgrade fixture would erase authenticated succession history")
	}
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, prefix := range []string{"quota_owner_succession", "quota_owner_succession_reference"} {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			if _, err = tx.Exec("DROP TRIGGER " + prefix + "_" + op); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, name := range []string{"owner_succession_authorizations_immutable_update", "owner_succession_authorizations_immutable_delete", "owner_succession_authorizations_predecessor", "execution_owner_succession_reference_immutable", "execution_owner_succession_retirement", "execution_owner_succession_insert", "execution_owner_history_no_delete", "execution_owner_retired_immutable", "execution_owner_identity_immutable", "owner_control_history_no_delete", "owner_control_history_immutable"} {
		if _, err = tx.Exec("DROP TRIGGER " + name); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{`DROP INDEX execution_owner_succession_authorization`, `DROP INDEX owner_succession_launch`, `ALTER TABLE execution_owners DROP COLUMN succession_authorization_id`, `DROP TABLE owner_succession_authorizations`, `UPDATE schema_meta SET version=24`} {
		if _, err = tx.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerSuccessionSchemaMigrationPreservesRowsAndRollsBack(t *testing.T) {
	for _, fault := range []string{"", "validated", "objects", "triggers", "version", "commit"} {
		t.Run(fmt.Sprintf("stage_%s", fault), func(t *testing.T) {
			f, i, _ := originalOwnerBound(t)
			downgradeOwnerSuccessionSchema25(t, f.db)
			before := collectionUsage(t, f.db.sql, "global", "")
			path := f.db.path
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			ownerSuccessionSchema25Hook = func(stage string) error {
				if stage == fault {
					return fmt.Errorf("injected-schema25-%s", stage)
				}
				return nil
			}
			t.Cleanup(func() { ownerSuccessionSchema25Hook = nil })
			opened, err := Open(path)
			ownerSuccessionSchema25Hook = nil
			if fault != "" {
				if err == nil {
					opened.Close()
					t.Fatal("fault did not roll back migration")
				}
				raw, e := sql.Open("sqlite", path)
				if e != nil {
					t.Fatal(e)
				}
				if originalOwnerSchemaCount(t, raw, `SELECT version FROM schema_meta`) != 24 || originalOwnerSchemaCount(t, raw, `SELECT COUNT(*) FROM sqlite_master WHERE name='owner_succession_authorizations'`) != 0 || originalOwnerSchemaCount(t, raw, `SELECT COUNT(*) FROM pragma_table_info('execution_owners') WHERE name='succession_authorization_id'`) != 0 {
					t.Fatal("migration failure leaked schema25 objects")
				}
				if collectionUsage(t, raw, "global", "") != before {
					t.Fatal("failed migration changed accounting")
				}
				raw.Close()
				opened, err = Open(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, e := opened.InspectOriginalOwner(context.Background(), i.Scope.OwnerID)
			if e != nil || got.Intent != i || !got.IsCurrent || got.Phase != "bound" {
				t.Fatal("migration rewrote original owner", e)
			}
			if collectionUsage(t, opened.sql, "global", "") != before || originalOwnerSchemaCount(t, opened.sql, `SELECT COUNT(*) FROM pragma_foreign_key_check`) != 0 {
				t.Fatal("migration changed accounting or FK")
			}
			opened.Close()
			again, e := Open(path)
			if e != nil {
				t.Fatal(e)
			}
			defer again.Close()
			if collectionUsage(t, again.sql, "global", "") != before {
				t.Fatal("reopen repeated migration accounting")
			}
		})
	}
}

func TestOwnerSuccessionSchemaRetainsUnprovedIntendedOwner(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	i, err := f.db.PrepareOriginalOwner(context.Background(), f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec(`UPDATE execution_owners SET is_current=0 WHERE owner_id=?`, i.Scope.OwnerID); err == nil {
		t.Fatal("intended slot released without original child exit proof")
	}
	state, err := f.db.InspectOriginalOwner(context.Background(), i.Scope.OwnerID)
	if err != nil || !state.IsCurrent || state.Phase != "intended" || state.Binding != nil {
		t.Fatal("unproved intended owner changed", err)
	}
}

func ownerSchemaTextCost(t *testing.T, db *DB, table, key, id string, fixed int64, columns []string) int64 {
	t.Helper()
	values := make([]string, len(columns))
	dest := make([]any, len(columns))
	expr := make([]string, len(columns))
	for i, col := range columns {
		dest[i] = &values[i]
		expr[i] = "COALESCE(" + col + ",'')"
	}
	if err := db.sql.QueryRow("SELECT "+strings.Join(expr, ",")+" FROM "+table+" WHERE "+key+"=?", id).Scan(dest...); err != nil {
		t.Fatal(err)
	}
	for _, v := range values {
		fixed += int64(len([]byte(v)))
	}
	return fixed
}

func TestOwnerSuccessionSchemaAccountsUTF8AndImmutableAuthorization(t *testing.T) {
	f, i, _ := originalOwnerBound(t)
	next := i
	next.Scope.OwnerID = "successor-界"
	next.Scope.Incarnation = 2
	next.Scope.HostGeneration = "generation-界"
	next.BootstrapID = "successor-bootstrap"
	next.RequestID = "successor-prepare"
	prepareOwnerSuccessionSchemaTask(t, f.db, i.Scope.LaunchID)
	global := collectionUsage(t, f.db.sql, "global", "")
	run := collectionUsage(t, f.db.sql, "run", i.Scope.RunID)
	auth := insertOwnerSuccessionSchemaAuthorization(t, f.db, i, next)
	// Eight numeric cells cost64; byte lengths are calculated independently in
	// Go from persisted fields, including non-ASCII authorization/generation IDs.
	cost := ownerSchemaTextCost(t, f.db, "owner_succession_authorizations", "authorization_id", auth, 64, []string{"authorization_id", "command_id", "request_hash", "run_id", "task_id", "launch_id", "predecessor_owner_id", "predecessor_scope_hash", "owner_birth", "owner_exit_proof", "owner_exited_at", "host_birth", "host_exit_proof", "host_exited_at", "successor_origin_context_id", "successor_origin_birth", "successor_host_generation", "successor_executable", "successor_executable_sha256", "created_at"})
	if collectionUsage(t, f.db.sql, "global", "") != global+cost || collectionUsage(t, f.db.sql, "run", i.Scope.RunID) != run+cost {
		t.Fatal("authorization UTF-8 accounting mismatch")
	}
	for _, q := range []string{`UPDATE owner_succession_authorizations SET command_id='changed' WHERE authorization_id=?`, `DELETE FROM owner_succession_authorizations WHERE authorization_id=?`} {
		if _, err := f.db.sql.Exec(q, auth); err == nil {
			t.Fatal("immutable authorization mutated")
		}
	}
	if collectionUsage(t, f.db.sql, "global", "") != global+cost {
		t.Fatal("rejected authorization mutation changed quota")
	}
	if _, err := f.db.sql.Exec(`UPDATE execution_owners SET phase='retired',is_current=0 WHERE owner_id=?`, i.Scope.OwnerID); err != nil {
		t.Fatal(err)
	}
	if err := insertOwnerSchemaSuccessor(f.db, next, auth); err != nil {
		t.Fatal(err)
	}
	rowCost := ownerSchemaTextCost(t, f.db, "execution_owners", "owner_id", next.Scope.OwnerID, 72, []string{"owner_id", "launch_id", "run_id", "producer_id", "bootstrap_id", "bootstrap_token_hash", "coordinator_key_id", "request_id", "request_hash", "intent_json", "phase", "owner_birth", "owner_public_key", "current_host_birth", "original_binding_json", "original_binding_hash", "created_at", "updated_at"})
	want := cost + 2 + rowCost + 8 + int64(len([]byte(auth))) // bound→retired increases phase by two bytes.
	if collectionUsage(t, f.db.sql, "global", "") != global+want || collectionUsage(t, f.db.sql, "run", i.Scope.RunID) != run+want {
		t.Fatal("successor row/reference accounting mismatch")
	}
	if _, err := f.db.sql.Exec(`UPDATE execution_owners SET succession_authorization_id=NULL WHERE owner_id=?`, next.Scope.OwnerID); err == nil {
		t.Fatal("successor detached immutable authorization")
	}
	if _, err := f.db.sql.Exec(`UPDATE execution_owners SET is_current=1,phase='bound' WHERE owner_id=?`, i.Scope.OwnerID); err == nil {
		t.Fatal("retired owner resurrected")
	}
	if originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM pragma_foreign_key_check`) != 0 {
		t.Fatal("succession broke child foreign keys")
	}
}

func TestOwnerSuccessionSchemaRejectsForeignOrSkippedSuccessor(t *testing.T) {
	for _, kind := range []string{"authorization", "incarnation", "origin_context", "origin_pid", "origin_birth", "generation", "executable", "digest"} {
		t.Run(kind, func(t *testing.T) {
			f, i, _ := originalOwnerBound(t)
			next := i
			next.Scope.OwnerID = "next"
			next.Scope.Incarnation = 2
			next.Scope.HostGeneration = "next-generation"
			next.BootstrapID = "next-bootstrap"
			next.RequestID = "next-prepare"
			auth := insertOwnerSuccessionSchemaAuthorization(t, f.db, i, next)
			if _, err := f.db.sql.Exec(`UPDATE execution_owners SET phase='retired',is_current=0 WHERE owner_id=?`, i.Scope.OwnerID); err != nil {
				t.Fatal(err)
			}
			before := collectionUsage(t, f.db.sql, "global", "")
			switch kind {
			case "authorization":
				auth = "foreign"
			case "incarnation":
				next.Scope.Incarnation = 3
			case "origin_context":
				next.Scope.OriginalOriginContextID = "foreign"
			case "origin_pid":
				next.Scope.OriginalOriginPID++
			case "origin_birth":
				next.Scope.OriginalOriginBirth = "foreign"
			case "generation":
				next.Scope.HostGeneration = "foreign"
			case "executable":
				next.Scope.Executable = "/foreign"
			case "digest":
				next.Scope.ExecutableSHA256 = strings.Repeat("a", 64)
			}
			if err := insertOwnerSchemaSuccessor(f.db, next, auth); err == nil {
				t.Fatal("foreign or skipped successor admitted")
			}
			if originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM execution_owners`) != 1 || collectionUsage(t, f.db.sql, "global", "") != before {
				t.Fatal("rejected successor changed row/accounting")
			}
		})
	}
}

func TestOwnerSuccessionSchemaRejectsUnprovenLegacyRetirement(t *testing.T) {
	f, i, _ := originalOwnerBound(t)
	downgradeOwnerSuccessionSchema25(t, f.db)
	if _, err := f.db.sql.Exec(`UPDATE execution_owners SET phase='retired',is_current=0 WHERE owner_id=?`, i.Scope.OwnerID); err != nil {
		t.Fatal(err)
	}
	before := collectionUsage(t, f.db.sql, "global", "")
	path := f.db.path
	f.db.Close()
	if db, err := Open(path); err == nil {
		db.Close()
		t.Fatal("migration invented authorization for unproved historical retirement")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if originalOwnerSchemaCount(t, raw, `SELECT version FROM schema_meta`) != 24 || collectionUsage(t, raw, "global", "") != before || originalOwnerSchemaCount(t, raw, `SELECT COUNT(*) FROM execution_owners WHERE phase='retired' AND is_current=0`) != 1 {
		t.Fatal("rejected history migration rewrote evidence")
	}
}
