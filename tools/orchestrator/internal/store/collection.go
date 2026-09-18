package store

import (
	"context"
	"encoding/json"
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
	mode, err := d.DeliveryMode(ctx, task)
	if err != nil {
		return "", "", err
	}
	if mode != "collect" || len(events) == 0 {
		return "", "", nil
	}
	bindings := make([]collectionBinding, 0, len(events))
	for _, e := range events {
		if e.ActionSlot != "" && e.Kind != "progress" {
			bindings = append(bindings, collectionBinding{e.EventID, e.EventRevision, e.PayloadHash, e.ActionSlot})
		}
	}
	if len(bindings) == 0 {
		return "", "", nil
	}
	raw, err := json.Marshal(bindings)
	if err != nil {
		return "", "", err
	}
	proof := runtimeHash("collection-v1\x00" + task + "\x00" + string(raw))
	id := "collection-" + proof
	_, err = d.sql.ExecContext(ctx, `INSERT INTO collection_receipts(id,task_id,proof_hash,bindings_json) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING`, id, task, proof, string(raw))
	return id, proof, err
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
