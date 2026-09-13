package nativebridgecli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDriverEnvironmentPreservesSourceAndStripsToolCapabilities(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://synthetic-proxy.invalid")
	t.Setenv("PROVIDER_SESSION_MARKER", "synthetic-source-session")
	t.Setenv("ORCHESTRATOR_CONTROL_TOKEN", "must-not-cross")
	t.Setenv("ORCHESTRATOR_REPORT_CAPABILITY", "must-not-cross")
	t.Setenv("ORCHESTRATOR_ENABLE_TEST_FAKE", "1")
	values := make(map[string]string)
	for _, value := range driverEnvironment(false) {
		for index, character := range value {
			if character == '=' {
				values[value[:index]] = value[index+1:]
				break
			}
		}
	}
	if values["HTTPS_PROXY"] != "http://synthetic-proxy.invalid" ||
		values["PROVIDER_SESSION_MARKER"] != "synthetic-source-session" {
		t.Fatalf("source environment missing: %#v", values)
	}
	for _, key := range []string{"ORCHESTRATOR_CONTROL_TOKEN", "ORCHESTRATOR_REPORT_CAPABILITY", "ORCHESTRATOR_ENABLE_TEST_FAKE"} {
		if _, exists := values[key]; exists {
			t.Fatalf("tool capability escaped: %s", key)
		}
	}
	if fake := driverEnvironment(true); !containsEnvironment(fake, "ORCHESTRATOR_ENABLE_TEST_FAKE=1") {
		t.Fatal("authorized test fake missing")
	}
}

func containsEnvironment(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

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
