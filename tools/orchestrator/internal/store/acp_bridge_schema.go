package store

import (
	"database/sql"
	"fmt"
)

// schema23MigrationHook is test-only fault injection for the committed
// migration stages. Production leaves it nil.
var schema23MigrationHook func(string) error

func migrateACPBridgeSchema(tx *sql.Tx) error {
	// Only the parent and attempt quota formulas change in schema23. Child
	// triggers retain their original DDL and accounting semantics.
	for _, name := range []string{"bridge_bindings", "bridge_delivery_attempts"} {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			if _, err := tx.Exec(`DROP TRIGGER quota_` + name + `_` + op); err != nil {
				return err
			}
		}
	}
	childTriggers, err := snapshotAndDropSchema23ChildQuotaTriggers(tx)
	if err != nil {
		return err
	}
	if err := schema23MigrationStage("create"); err != nil {
		return err
	}
	if _, err := tx.Exec(`
CREATE TABLE bridge_bindings_schema23(
  binding_id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), provider_id TEXT NOT NULL,
  native_session_id TEXT NOT NULL DEFAULT '', owner_context_id TEXT NOT NULL,
  owner_pid INTEGER NOT NULL, owner_birth TEXT NOT NULL, native_pid INTEGER NOT NULL,
  native_birth TEXT NOT NULL, native_executable_sha256 TEXT NOT NULL, owner_epoch INTEGER NOT NULL CHECK(owner_epoch>0),
  role TEXT NOT NULL CHECK(role='owner'), transport_mode TEXT NOT NULL CHECK(transport_mode IN ('hook_pull','grok_acp_push_v1')),
  manifest_sha256 TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('PENDING_SESSION','SESSION_OBSERVED','ACTIVE','DETACHED','STALE')),
  candidate_observation_event_id TEXT NOT NULL DEFAULT '', candidate_observation_hash TEXT NOT NULL DEFAULT '',
  hook_token_hash TEXT NOT NULL, capability_file TEXT NOT NULL, request_id TEXT NOT NULL,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
)`); err != nil {
		return err
	}
	if err := schema23MigrationStage("copy"); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO bridge_bindings_schema23(binding_id,run_id,provider_id,native_session_id,owner_context_id,owner_pid,owner_birth,native_pid,native_birth,native_executable_sha256,owner_epoch,role,transport_mode,manifest_sha256,status,candidate_observation_event_id,candidate_observation_hash,hook_token_hash,capability_file,request_id,created_at,updated_at) SELECT binding_id,run_id,provider_id,native_session_id,owner_context_id,owner_pid,owner_birth,native_pid,native_birth,native_executable_sha256,owner_epoch,role,transport_mode,manifest_sha256,status,candidate_observation_event_id,candidate_observation_hash,hook_token_hash,capability_file,request_id,created_at,updated_at FROM bridge_bindings`); err != nil {
		return err
	}
	if err := schema23MigrationStage("drop"); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE bridge_bindings`); err != nil {
		return err
	}
	if err := schema23MigrationStage("rename"); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE bridge_bindings_schema23 RENAME TO bridge_bindings`); err != nil {
		return err
	}
	if err := schema23MigrationStage("recreate"); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE UNIQUE INDEX bridge_one_live_binding ON bridge_bindings(run_id,owner_context_id) WHERE status IN ('PENDING_SESSION','SESSION_OBSERVED','ACTIVE'); CREATE UNIQUE INDEX bridge_binding_epoch ON bridge_bindings(run_id,owner_context_id,owner_epoch)`); err != nil {
		return err
	}
	for _, trigger := range childTriggers {
		if _, err := tx.Exec(trigger.ddl); err != nil {
			return err
		}
	}
	for _, column := range []string{
		`acp_session_id TEXT NOT NULL DEFAULT ''`,
		`acp_turn_ordinal INTEGER CHECK(acp_turn_ordinal IS NULL OR acp_turn_ordinal>0)`,
		`acp_rpc_request_id INTEGER CHECK(acp_rpc_request_id IS NULL OR acp_rpc_request_id>0)`,
		`acp_response_sha256 TEXT NOT NULL DEFAULT ''`,
		`acp_stop_reason TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := tx.Exec(`ALTER TABLE bridge_delivery_attempts ADD COLUMN ` + column); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`CREATE UNIQUE INDEX bridge_acp_delivery_ordinal ON bridge_delivery_attempts(binding_id,acp_turn_ordinal) WHERE acp_turn_ordinal IS NOT NULL; CREATE UNIQUE INDEX bridge_acp_delivery_request ON bridge_delivery_attempts(binding_id,acp_rpc_request_id) WHERE acp_rpc_request_id IS NOT NULL`); err != nil {
		return err
	}
	if err := bridgeQuotaTriggersWithTables(tx, bridgeQuotaTables, "bridge_bindings"); err != nil {
		return err
	}
	if err := bridgeQuotaTriggers23(tx); err != nil {
		return err
	}
	if err := schema23MigrationStage("trigger"); err != nil {
		return err
	}
	return nil
}

type schema23TriggerDDL struct {
	name string
	ddl  string
}

// modernc validates child trigger bodies while bridge_bindings is absent.
// Preserve their exact DDL rather than regenerating unrelated quota semantics.
func snapshotAndDropSchema23ChildQuotaTriggers(tx *sql.Tx) ([]schema23TriggerDDL, error) {
	names := []string{
		"quota_bridge_ingress_INSERT", "quota_bridge_ingress_UPDATE", "quota_bridge_ingress_DELETE",
		"quota_bridge_batches_INSERT", "quota_bridge_batches_UPDATE", "quota_bridge_batches_DELETE",
		"quota_bridge_batch_items_INSERT", "quota_bridge_batch_items_UPDATE", "quota_bridge_batch_items_DELETE",
	}
	out := make([]schema23TriggerDDL, 0, len(names))
	for _, name := range names {
		var ddl string
		if err := tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&ddl); err != nil {
			return nil, fmt.Errorf("schema23 snapshot %s: %w", name, err)
		}
		out = append(out, schema23TriggerDDL{name: name, ddl: ddl})
	}
	for _, trigger := range out {
		if _, err := tx.Exec(`DROP TRIGGER ` + trigger.name); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func schema23MigrationStage(stage string) error {
	if schema23MigrationHook != nil {
		return schema23MigrationHook(stage)
	}
	return nil
}
