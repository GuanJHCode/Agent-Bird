package nativebridgecli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func driverRequestFixture(t *testing.T) (string, driverRequest) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"driver", "bridge", "owners", "g1", "g0"} {
		if err = os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	interpreter := filepath.Join(root, "python")
	if err = os.WriteFile(interpreter, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	packageRoot := filepath.Join(root, "package")
	if err = os.MkdirAll(filepath.Join(packageRoot, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(packageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(packageRoot, "scripts", "native-product-bridge.py")
	if err = os.WriteFile(script, []byte("# installed bridge\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(packageRoot, "manifest.json")
	privateJSON(t, manifest, map[string]any{"schema_version": 1, "files": []map[string]any{{
		"path": "scripts/native-product-bridge.py", "sha256": fileSHA(t, script), "mode": 0o700,
	}}})
	request := driverRequest{
		Version: 1, Runtime: driverRuntime{Version: 1, PackageRoot: packageRoot,
			PackageManifest: manifest, PackageManifestSHA256: fileSHA(t, manifest),
			Interpreter: interpreter, InterpreterSHA256: fileSHA(t, interpreter),
			G0RuntimeRoot: filepath.Join(root, "g0"), G0RuntimeManifest: filepath.Join(root, "g0", "manifest.json")},
		DriverRoot: filepath.Join(root, "driver"), ManifestPath: filepath.Join(root, "service.json"),
		ManifestSHA256: strings.Repeat("a", 64), ActivationID: strings.Repeat("b", 32),
		OwnerReadyReceipt: "ready-abcdef012345.json", OwnerReadySHA256: strings.Repeat("c", 64),
		ProfileID: "profile-a", PublicSocket: filepath.Join(root, "public.sock"),
		BridgeRoot: filepath.Join(root, "bridge"), OwnerCapabilityRoot: filepath.Join(root, "owners"),
		G1State: filepath.Join(root, "g1"), Mode: "submit", SubmitRequest: filepath.Join(root, "submit.json"),
		TaskID: "task-a", BootstrapTimeoutSeconds: 300, WatchPolicy: "until_terminal_or_owner_detached",
	}
	requestPath := filepath.Join(root, "driver-request.json")
	privateJSON(t, requestPath, request)
	return requestPath, request
}

func TestDriverRequestSeparatesBootstrapTimeoutFromWatchLifetime(t *testing.T) {
	requestPath, _ := driverRequestFixture(t)
	request, err := loadDriverRequest(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	if request.BootstrapTimeoutSeconds != 300 || request.WatchPolicy != "until_terminal_or_owner_detached" {
		t.Fatalf("request=%+v", request)
	}
}

func TestStartDriverDoesNotReportStaleWatchingDriverAsLive(t *testing.T) {
	requestPath, request := driverRequestFixture(t)
	privateJSON(t, filepath.Join(request.DriverRoot, "launcher.json"), driverLauncherReceipt{
		Version: 1, PID: os.Getpid(), Birth: "stale-birth", RequestSHA256: fileSHA(t, requestPath),
	})
	privateJSON(t, filepath.Join(request.DriverRoot, "status.json"), map[string]any{
		"version": 1, "status": "watching", "driver_root": request.DriverRoot,
	})
	response, err := StartDriver(context.Background(), requestPath)
	if err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	if err = json.Unmarshal(response, &status); err != nil {
		t.Fatal(err)
	}
	if status["status"] != "driver_dead" || status["recoverable"] != true || status["previous_status"] != "watching" {
		t.Fatalf("status=%s", response)
	}
}

func TestStartDriverPreservesOwnerDetachedTerminalStatus(t *testing.T) {
	requestPath, request := driverRequestFixture(t)
	privateJSON(t, filepath.Join(request.DriverRoot, "launcher.json"), driverLauncherReceipt{
		Version: 1, PID: os.Getpid(), Birth: "stale-birth", RequestSHA256: fileSHA(t, requestPath),
	})
	privateJSON(t, filepath.Join(request.DriverRoot, "status.json"), map[string]any{
		"version": 1, "status": "owner_detached", "driver_root": request.DriverRoot,
	})
	response, err := StartDriver(context.Background(), requestPath)
	if err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	if err = json.Unmarshal(response, &status); err != nil {
		t.Fatal(err)
	}
	if status["status"] != "owner_detached" {
		t.Fatalf("status=%s", response)
	}
}

func TestDriverEnvironmentPreservesSourceAndStripsToolCapabilities(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://synthetic-proxy.invalid")
	t.Setenv("PROVIDER_SESSION_MARKER", "synthetic-source-session")
	t.Setenv("ORCHESTRATOR_CONTROL_TOKEN", "must-not-cross")
	t.Setenv("ORCHESTRATOR_REPORT_CAPABILITY", "must-not-cross")
	t.Setenv("ORCHESTRATOR_FUTURE_SLOT", "must-not-cross")
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
	for _, key := range []string{"ORCHESTRATOR_CONTROL_TOKEN", "ORCHESTRATOR_REPORT_CAPABILITY", "ORCHESTRATOR_FUTURE_SLOT", "ORCHESTRATOR_ENABLE_TEST_FAKE"} {
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
