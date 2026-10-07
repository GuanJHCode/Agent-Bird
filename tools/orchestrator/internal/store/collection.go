package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type collectionBinding struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Hash     string `json:"hash"`
	Slot     string `json:"slot"`
}

func (d *DB) DeliveryMode(ctx context.Context, task string) (string, error) {
	var mode string
	err := d.sql.QueryRowContext(ctx, `SELECT r.delivery_mode FROM runs r JOIN tasks t ON t.run_id=r.id WHERE t.id=?`, task).Scan(&mode)
	return mode, err
}

// Collection receipts bind the exact delivered page, not a business acceptance
// or proof of insertion into a native conversation history.
func (d *DB) CollectionReceipt(ctx context.Context, task string, events []contract.Event) (string, string, error) {
	if err := d.bridgeLockContext(ctx); err != nil {
		return "", "", err
	}
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	var run, mode string
	if err = tx.QueryRowContext(ctx, `SELECT r.id,r.delivery_mode FROM runs r JOIN tasks t ON t.run_id=r.id WHERE t.id=?`, task).Scan(&run, &mode); err != nil {
		return "", "", err
	}
	if mode != "collect" || len(events) == 0 {
		return "", "", nil
	}
	incoming, err := collectionReceiptIncoming(ctx, tx, task, events)
	if err != nil {
		return "", "", err
	}
	if incoming > 0 {
		if err = d.storageAdmission(ctx, tx, run, incoming, false); err != nil {
			return "", "", err
		}
	}
	id, proof, err := collectionReceipt(ctx, tx, task, events)
	if err != nil {
		return "", "", err
	}
	if err = tx.Commit(); err != nil {
		return "", "", err
	}
	return id, proof, nil
}

type collectionReceiptRow struct {
	id, task, proof, raw string
	exists               bool
}

func prepareCollectionReceipt(ctx context.Context, tx *sql.Tx, task string, events []contract.Event) (collectionReceiptRow, error) {
	r := collectionReceiptRow{task: task}
	var err error
	r.id, r.proof, r.raw, err = collectionReceiptParts(task, events)
	if err != nil || r.id == "" {
		return r, err
	}
	var existingTask, existingProof, existingRaw string
	err = tx.QueryRowContext(ctx, `SELECT task_id,proof_hash,bindings_json FROM collection_receipts WHERE id=?`, r.id).Scan(&existingTask, &existingProof, &existingRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if existingTask != task || existingProof != r.proof || existingRaw != r.raw {
		return r, ErrConflict
	}
	r.exists = true
	return r, nil
}

// The caller aggregates this cost with any other writes before admission in
// the same Store-locked transaction. Only actual INSERT triggers charge usage.
func collectionReceiptIncoming(ctx context.Context, tx *sql.Tx, task string, events []contract.Event) (int64, error) {
	r, err := prepareCollectionReceipt(ctx, tx, task, events)
	if err != nil {
		return 0, err
	}
	if r.id == "" || r.exists {
		return 0, nil
	}
	return int64(len(r.id) + len(r.task) + len(r.proof) + len(r.raw)), nil
}

func collectionReceipt(ctx context.Context, tx *sql.Tx, task string, events []contract.Event) (string, string, error) {
	r, err := prepareCollectionReceipt(ctx, tx, task, events)
	if err != nil || r.id == "" {
		return r.id, r.proof, err
	}
	if r.exists {
		return r.id, r.proof, nil
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO collection_receipts(id,task_id,proof_hash,bindings_json) VALUES(?,?,?,?)`, r.id, r.task, r.proof, r.raw)
	if err != nil {
		return "", "", mapConflict(err)
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		return "", "", ErrConflict
	}
	return r.id, r.proof, nil
}

func collectionReceiptParts(task string, events []contract.Event) (string, string, string, error) {
	bindings := make([]collectionBinding, 0, len(events))
	for _, e := range events {
		if e.ActionSlot != "" && e.Kind != "progress" {
			bindings = append(bindings, collectionBinding{e.EventID, e.EventRevision, e.PayloadHash, e.ActionSlot})
		}
	}
	if len(bindings) == 0 {
		return "", "", "", nil
	}
	raw, err := json.Marshal(bindings)
	if err != nil {
		return "", "", "", err
	}
	proof := runtimeHash("collection-v1\x00" + task + "\x00" + string(raw))
	id := "collection-" + proof
	return id, proof, string(raw), nil
}
func (d *DB) AckDelivery(ctx context.Context, task, id, historyProof, collectionProof string, decisions []AckDecision) error {
	mode, err := d.DeliveryMode(ctx, task)
	if err != nil {
		return err
	}
	if mode == "native" {
		if collectionProof != "" {
			return CodeError("delivery_proof_mode_mismatch")
		}
		return d.AckDecisions(ctx, task, id, historyProof, decisions)
	}
	if historyProof != "" || !validDigest(collectionProof) {
		return CodeError("delivery_proof_mode_mismatch")
	}
	var proof, raw string
	if err = d.sql.QueryRowContext(ctx, `SELECT proof_hash,bindings_json FROM collection_receipts WHERE id=? AND task_id=?`, id, task).Scan(&proof, &raw); err != nil || proof != strings.ToLower(collectionProof) {
		return ErrConflict
	}
	var bindings []collectionBinding
	if json.Unmarshal([]byte(raw), &bindings) != nil || len(bindings) != len(decisions) {
		return ErrConflict
	}
	expected := map[string]collectionBinding{}
	for _, b := range bindings {
		expected[b.ID] = b
	}
	for _, d := range decisions {
		if expected[d.EventID] != (collectionBinding{d.EventID, d.EventRevision, d.EventHash, d.ActionSlot}) {
			return ErrConflict
		}
	}
	return d.AckDecisions(ctx, task, id, collectionProof, decisions)
}
