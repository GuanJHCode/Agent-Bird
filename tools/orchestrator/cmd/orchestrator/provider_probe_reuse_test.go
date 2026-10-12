package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
)

func TestProviderDiscoverySharesPinnedHelpWithPreflight(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "provider")
	script := []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 1.0; else echo hit >> \"${0}.help\"; echo --output-format --input-format --permission-mode --disallowed-tools --model --effort --resume --json-schema; fi\n")
	if err := os.WriteFile(binary, script, 0700); err != nil {
		t.Fatal(err)
	}
	if err := prepareFixtureFirstExec(binary); err != nil {
		t.Fatal(err)
	}
	req := filepath.Join(root, "request.json")
	body, _ := json.Marshal(providerRequest{Provider: adapter.ProviderClaude, BinaryPath: binary})
	if err := os.WriteFile(req, body, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := adapter.WithProbeCache(context.Background())
	var output bytes.Buffer
	if err := providerControl(ctx, "provider-probe", []string{"--request", req}, &output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Lock adapter.ProviderLock `json:"lock"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if err := adapter.VerifyExecutable(result.Lock.Binary, result.Lock.Binary.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ProbeOutputPinned(ctx, result.Lock.Binary, "--help"); err != nil {
		t.Fatal(err)
	}
	hits, err := os.ReadFile(binary + ".help")
	if err != nil || string(hits) != "hit\n" {
		t.Fatalf("help executed more than once: %q %v", hits, err)
	}
	if _, err := adapter.ProbeOutputPinned(adapter.WithProbeCache(context.Background()), result.Lock.Binary, "--help"); err != nil {
		t.Fatal(err)
	}
	hits, err = os.ReadFile(binary + ".help")
	if err != nil || string(hits) != "hit\nhit\n" {
		t.Fatal("cache escaped its request", string(hits), err)
	}
}
