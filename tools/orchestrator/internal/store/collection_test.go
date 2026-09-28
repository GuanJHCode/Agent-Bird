package store

import (
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"context"
	"testing"
)

func TestCollectionReceiptExcludesNonActionableDiagnostics(t *testing.T) {
	db, _ := quotaFixture(t)
	ctx := context.Background()
	if _, err := db.sql.Exec(`UPDATE runs SET delivery_mode='collect' WHERE id='run'`); err != nil {
		t.Fatal(err)
	}
	result := contract.Event{EventID: "result", Kind: "result", EventRevision: 1, ActionSlot: "result-slot", PayloadHash: "hash"}
	diagnostic := contract.Event{EventID: "progress", Kind: "progress", EventRevision: 1, ActionSlot: "internal-slot", PayloadHash: "progress-hash"}
	id, proof, err := db.CollectionReceipt(ctx, "task", []contract.Event{diagnostic, result})
	if err != nil {
		t.Fatal(err)
	}
	expected, expectedProof, err := db.CollectionReceipt(ctx, "task", []contract.Event{result})
	if err != nil {
		t.Fatal(err)
	}
	if id != expected || proof != expectedProof {
		t.Fatal("diagnostic forces an impossible business ACK")
	}
}
