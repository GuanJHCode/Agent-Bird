package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestPlatformCapabilitiesReadOnlyContract(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if err := run(context.Background(), []string{"platform", "capabilities"}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Version         int             `json:"version"`
		SchemaVersion   int             `json:"schema_version"`
		RuntimeVerified bool            `json:"runtime_verified"`
		Contract        json.RawMessage `json:"contract"`
		ContractSHA256  string          `json:"contract_sha256"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Version != 1 || report.SchemaVersion < 41 || report.RuntimeVerified || diagnostics.Len() != 0 {
		t.Fatalf("invalid report: %s", out.String())
	}
	sum := sha256.Sum256(append([]byte("agent-bird/platform-contract/v1\n"), report.Contract...))
	if report.ContractSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("contract hash mismatch")
	}
	var contract struct {
		Capabilities []struct{ ID, Gate, Reason string } `json:"capabilities"`
	}
	if err := json.Unmarshal(report.Contract, &contract); err != nil {
		t.Fatal(err)
	}
	gates := map[string]string{}
	for _, c := range contract.Capabilities {
		if c.ID == "" || gates[c.ID] != "" || c.Reason == "" {
			t.Fatal("invalid capability")
		}
		gates[c.ID] = c.Gate
	}
	for _, id := range []string{"codex_worker", "filtered_compose", "filtered_rework", "filtered_retry", "filtered_continuation", "historical_wait_reconstruction"} {
		if gates[id] != "blocked" {
			t.Fatalf("%s gate=%s", id, gates[id])
		}
	}
}

func TestPlatformCapabilitiesRejectArguments(t *testing.T) {
	for _, args := range [][]string{{"platform"}, {"platform", "unknown"}, {"platform", "capabilities", "--enable"}} {
		var out, diagnostics bytes.Buffer
		if err := run(context.Background(), args, &out, &diagnostics); err == nil || out.Len() != 0 {
			t.Fatalf("accepted %v", args)
		}
	}
}
