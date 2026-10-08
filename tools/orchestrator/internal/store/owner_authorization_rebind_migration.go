package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// Schema29 allowed an origin transition to strand an immutable authorization.
// Adopt only its complete existing authenticated chain, never guessed history.
func (d *DB) backfillOwnerAuthorizationRebinds(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT run_id FROM owner_rebinds UNION SELECT run_id FROM owner_succession_authorizations UNION SELECT run_id FROM execution_owners WHERE is_current=1 AND (phase='intended' OR applied_control_revision=0)`)
	if err != nil {
		return err
	}
	var runs []string
	for rows.Next() {
		var run string
		if err = rows.Scan(&run); err != nil {
			break
		}
		runs = append(runs, run)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, run := range runs {
		origin, generation, e := ownerRebindTargetTx(ctx, tx, run)
		if e != nil {
			return e
		}
		var drift int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_owners o WHERE o.run_id=? AND o.is_current=1 AND (o.phase='intended' OR o.applied_control_revision=0) AND (json_extract(o.intent_json,'$.scope.original_origin_context_id') IS NOT ? OR json_extract(o.intent_json,'$.scope.original_origin_pid') IS NOT ? OR json_extract(o.intent_json,'$.scope.original_origin_birth') IS NOT ? OR json_extract(o.intent_json,'$.scope.host_generation') IS NOT ?)`, run, origin.OriginContextID, origin.OriginPID, origin.OriginBirth, generation).Scan(&drift); err != nil {
			return err
		}
		if drift != 0 {
			return CodeError("owner_succession_rebind_unproven_history")
		}
		a, e := pendingOwnerAuthorizationTx(ctx, tx, run)
		if e != nil {
			return e
		}
		var proofs []ownerAuthorizationRebindProof
		if a != nil {
			if a.InitialProofID != "" {
				var body string
				if err = tx.QueryRowContext(ctx, `SELECT facts_json FROM owner_initial_recoveries WHERE owner_id=?`, a.InitialProofID).Scan(&body); err != nil {
					return err
				}
				var facts InitialOwnerRecoveryFacts
				authority, e := d.initialOwnerRootAuthority()
				if e != nil || json.Unmarshal([]byte(body), &facts) != nil || !facts.valid() || facts.Root != authority.Nodes() {
					return CodeError("owner_initial_recovery_root_invalid")
				}
			}
			proofs, e = historicalOwnerAuthorizationRebinds(ctx, tx, *a, origin, generation)
			if e != nil {
				return e
			}
		}
		var oldBytes int64
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(`+originalOwnerBytesSQL("", ownerRebindTextColumns, 8)+`),0) FROM owner_rebinds WHERE run_id=?`, run).Scan(&oldBytes); err != nil {
			return err
		}
		incoming := oldBytes
		for _, p := range proofs {
			if incoming > int64(^uint64(0)>>1)-p.bytes() {
				return CodeError("control_spool_full")
			}
			incoming += p.bytes()
		}
		if incoming > 0 {
			if err = d.storageAdmission(ctx, tx, run, incoming, false); err != nil {
				return err
			}
			// Charge immediately, so the next run's global admission includes it.
			for _, scope := range []struct{ name, id string }{{"global", ""}, {"run", run}} {
				if _, err = tx.ExecContext(ctx, `INSERT INTO storage_usage(scope,scope_id,total_bytes,progress_bytes) VALUES(?,?,?,0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes`, scope.name, scope.id, oldBytes); err != nil {
					return err
				}
			}
			for _, p := range proofs {
				if err = insertOwnerAuthorizationRebindProof(ctx, tx, p); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func historicalOwnerAuthorizationRebinds(ctx context.Context, tx *sql.Tx, a OriginalOwnerSuccessionAuthorization, origin RunOwnerBinding, generation string) ([]ownerAuthorizationRebindProof, error) {
	reject := CodeError("owner_succession_rebind_unproven_history")
	if origin.OriginContextID != a.SuccessorOrigin.OriginContextID {
		return nil, reject
	}
	if generation == a.SuccessorHostGeneration {
		if origin != a.SuccessorOrigin {
			return nil, reject
		}
		return nil, nil
	}
	if _, err := successorOwnerPredecessor(ctx, tx, a); err != nil {
		return nil, err
	}
	var epoch uint64
	if err := tx.QueryRowContext(ctx, `SELECT epoch FROM coordinator WHERE id=1`).Scan(&epoch); err != nil {
		return nil, err
	}
	if epoch < a.CoordinatorEpoch {
		return nil, reject
	}
	var backward []ownerRebindRow
	seen := map[string]bool{}
	current := generation
	for current != a.SuccessorHostGeneration {
		if len(backward) >= 64 || seen[current] {
			return nil, reject
		}
		seen[current] = true
		r := ownerRebindRow{RunID: a.RunID, Generation: current}
		if err := tx.QueryRowContext(ctx, `SELECT old_host_generation,origin_pid,origin_birth,attachment_proof_sha256,created_at FROM owner_rebinds WHERE run_id=? AND host_generation=?`, a.RunID, current).Scan(&r.OldGeneration, &r.PID, &r.Birth, &r.Proof, &r.Created); err != nil {
			return nil, reject
		}
		if r.OldGeneration == "" || r.OldGeneration == current || r.PID <= 0 || r.Birth == "" || !recoveryHandoffDigest(r.Proof) || r.Created == "" {
			return nil, reject
		}
		if len(backward) == 0 && (r.PID != origin.OriginPID || r.Birth != origin.OriginBirth) {
			return nil, reject
		}
		backward = append(backward, r)
		current = r.OldGeneration
	}
	proofs := make([]ownerAuthorizationRebindProof, 0, len(backward))
	for i := len(backward) - 1; i >= 0; i-- {
		r := backward[i]
		proofs = append(proofs, ownerAuthorizationRebindProof{AuthorizationID: a.AuthorizationID, RunID: a.RunID, LaunchID: a.LaunchID, Hop: len(proofs) + 1, OldGeneration: r.OldGeneration, Generation: r.Generation, Origin: RunOwnerBinding{origin.OriginContextID, r.PID, r.Birth}, Proof: r.Proof, DerivationEpoch: epoch, SourceCreated: r.Created})
	}
	return proofs, nil
}
