package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestB06CapabilitiesCLIChild(t *testing.T) {
	if os.Getenv("B06_CAPABILITIES_CHILD") != "1" {
		return
	}
	var args []string
	if json.Unmarshal([]byte(os.Getenv("B06_CAPABILITIES_ARGS")), &args) != nil {
		os.Exit(8)
	}
	err := run(context.Background(), args, os.Stdout, os.Stderr)
	if err != nil {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]string{"error": errorCode(err)})
		os.Exit(2)
	}
	os.Exit(0)
}

func capabilityCLI(t *testing.T, args ...string) ([]byte, []byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestB06CapabilitiesCLIChild$")
	raw, _ := json.Marshal(args)
	cmd.Env = append(os.Environ(), "B06_CAPABILITIES_CHILD=1", "B06_CAPABILITIES_ARGS="+string(raw))
	var out, diag bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &diag
	err := cmd.Run()
	return out.Bytes(), diag.Bytes(), err
}

func TestB06CapabilitiesActualCLIRoute(t *testing.T) {
	for _, provider := range []string{"claude", "codex", "grok", "agy"} {
		out, diag, err := capabilityCLI(t, "bridge", "capabilities", "--provider", provider)
		var value struct {
			Provider         string `json:"provider"`
			NativeAcceptance string `json:"native_acceptance"`
			Runtime          struct {
				Status string `json:"status"`
			} `json:"runtime"`
		}
		if err != nil || len(diag) != 0 || json.Unmarshal(out, &value) != nil || value.Provider != provider || value.NativeAcceptance != "WAIT_AUTH" || value.Runtime.Status != "NOT_RUN" {
			t.Fatalf("actual route %s: %s %s %v", provider, out, diag, err)
		}
	}
}

func TestB06CapabilitiesActualCLIProbeAndPrivacy(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "codex")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n[ \"$#\" = 1 ] && [ \"$1\" = --version ] || exit 9\nprintf 'codex-cli 0.154.0\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	out, diag, err := capabilityCLI(t, "bridge", "capabilities", "--provider", "codex", "--binary", binary, "--probe-version")
	if err != nil || len(diag) != 0 || !bytes.Contains(out, []byte(`"status":"OBSERVED"`)) || !bytes.Contains(out, []byte(`"version":"0.154.0"`)) {
		t.Fatalf("probe route: %s %s %v", out, diag, err)
	}
	token, err := store.NewBridgeClaimToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s' '"+token+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	out, diag, err = capabilityCLI(t, "bridge", "capabilities", "--provider", "codex", "--binary", binary, "--probe-version")
	if err != nil || bytes.Contains(append(out, diag...), []byte(token)) || !bytes.Contains(out, []byte(`"reason":"version_output_invalid"`)) {
		t.Fatalf("factory token public leak: %s %s %v", out, diag, err)
	}
	secretPath := filepath.Join(root, token)
	if err := os.WriteFile(secretPath, []byte("#!/bin/sh\nprintf 'codex-cli 0.154.0'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	out, diag, err = capabilityCLI(t, "bridge", "capabilities", "--provider", "codex", "--binary", secretPath, "--probe-version")
	if err != nil || bytes.Contains(append(out, diag...), []byte(token)) || !bytes.Contains(out, []byte(`"reason":"executable_invalid"`)) {
		t.Fatalf("factory token executable leaked through actual CLI: %s %s %v", out, diag, err)
	}
	out, diag, err = capabilityCLI(t, "bridge", "capabilities", "--provider", token)
	if err == nil || bytes.Contains(append(out, diag...), []byte(token)) || strings.Contains(string(diag), "unknown provider ") {
		t.Fatalf("error reflected private input: %s %s %v", out, diag, err)
	}
}

func TestB06NativeRunRejectsParallelBridgePathsAndProofMix(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.CreateOwnerGrant(ctx, "managed-native-b06", os.Getpid(), birth)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{DeliveryMode: "native", ID: "native-run", ControllerThread: "managed-native-b06", PlanRevision: 1, OriginContextID: grant.ID, OriginPID: os.Getpid(), OriginBirth: birth}, Host: store.HostLaunchSpec{OriginContextID: grant.ID, HostGeneration: "generation", Executable: "/private/bin/host"}, Tasks: []store.TaskSpec{{ID: "native-task", RunID: "native-run", MaxAttempts: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	owner := store.WithBridgeOwnerProof(ctx, store.BridgeOwnerProof{RunID: "native-run", OwnerMode: "local", ControllerThread: "managed-native-b06", ControlToken: receipt.ControlToken, OwnerContextID: grant.ID, OwnerPID: os.Getpid(), OwnerBirth: birth, OwnerGrantID: grant.ID, OwnerGrantToken: grant.Token, PeerPID: os.Getpid(), PeerBirth: birth})
	var wg sync.WaitGroup
	for _, mode := range []string{"hook_pull", "attached_push", "explicit_collect"} {
		wg.Add(1)
		go func(mode string) {
			defer wg.Done()
			_, err := db.BindBridge(owner, contract.BridgeBindRequest{SchemaVersion: 1, RequestID: "bind-" + mode, RunID: "native-run", ProviderID: "claude", NativePID: os.Getpid(), NativeBirth: birth, NativeExecutableSHA256: strings.Repeat("a", 64), TransportMode: mode, OwnerMode: "local", ManifestSHA256: strings.Repeat("b", 64)})
			want := "bridge_invalid_input"
			if mode == "hook_pull" {
				want = "bridge_mode_unsupported"
			}
			if err == nil || err.Error() != want {
				t.Errorf("native mode %s expected %s, got %v", mode, want, err)
			}
		}(mode)
	}
	wg.Wait()
	for _, table := range []string{"bridge_bindings", "bridge_delivery_attempts"} {
		var n int
		if err := db.SQL().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("parallel path created %s count=%d err=%v", table, n, err)
		}
	}
	if err := db.AckDelivery(ctx, "native-task", "collection-test", "", strings.Repeat("c", 64), nil); err == nil || err.Error() != "delivery_proof_mode_mismatch" {
		t.Fatalf("native accepted collect proof: %v", err)
	}
	if _, err := db.SQL().Exec("UPDATE runs SET delivery_mode='collect' WHERE id='native-run'"); err != nil {
		t.Fatal(err)
	}
	if err := db.AckDelivery(ctx, "native-task", "d_test", strings.Repeat("d", 64), "", nil); err == nil || err.Error() != "delivery_proof_mode_mismatch" {
		t.Fatalf("collect accepted native proof: %v", err)
	}
}
