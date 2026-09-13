package nativebridgecli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordDriverDecisionBindsExactHistoryReceipt(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	deliveryID := "d_" + strings.Repeat("a", 40)
	proof := strings.Repeat("b", 64)
	historyPath := filepath.Join(root, "history-"+deliveryID+".json")
	privateJSON(t, historyPath, historyReceipt{Version: 1, Event: "history_ready", DriverRoot: root,
		DeliveryID: deliveryID, HistoryProofSHA256: proof, NativeEventIDs: []string{"e_result"}})
	requestPath := filepath.Join(root, "decision-request.json")
	privateJSON(t, requestPath, decisionRequest{Version: 1, DriverRoot: root, DeliveryID: deliveryID,
		HistoryReceipt: historyPath, HistoryReceiptSHA256: fileSHA(t, historyPath),
		HistoryProofSHA256: proof,
		Decisions:          map[string]DriverDecision{"e_result": {Decision: "handled", CommandID: "accept-result-1"}}})
	response, err := RecordDriverDecision(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if json.Unmarshal(response, &value) != nil || value["status"] != "decision_recorded" {
		t.Fatalf("response=%s", response)
	}
	decisionPath := filepath.Join(root, "decisions", deliveryID+".json")
	var decision map[string]any
	if err = readPrivateJSON(decisionPath, &decision); err != nil || decision["delivery_id"] != deliveryID {
		t.Fatalf("decision=%#v err=%v", decision, err)
	}
	if _, err = RecordDriverDecision(requestPath); err != nil {
		t.Fatalf("idempotent decision: %v", err)
	}
	request := decisionRequest{}
	raw, _ := os.ReadFile(requestPath)
	_ = json.Unmarshal(raw, &request)
	request.Decisions["e_result"] = DriverDecision{Decision: "rejected", CommandID: "reject-result-1"}
	conflictPath := filepath.Join(root, "decision-conflict.json")
	privateJSON(t, conflictPath, request)
	if _, err = RecordDriverDecision(conflictPath); err == nil || err.Error() != "driver_decision_conflict" {
		t.Fatalf("conflict err=%v", err)
	}
}
