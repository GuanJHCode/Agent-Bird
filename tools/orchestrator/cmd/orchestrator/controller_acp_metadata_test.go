package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestControllerACPFlagCombinations(t *testing.T) {
	for _, tc := range []struct {
		name, provider, plan, handle, request string
		write                                 bool
		args                                  []string
		wantACP, wantError                    bool
	}{
		{name: "plain_grok", provider: "grok", args: []string{"chat"}},
		{name: "plain_claude", provider: "claude"},
		{name: "hook_claude", provider: "claude", plan: "plan", handle: "handle"},
		{name: "hook_agy", provider: "agy", plan: "plan", handle: "handle"},
		{name: "explicit_acp", provider: "grok", plan: "plan", handle: "handle", request: "request", write: true, wantACP: true},
		{name: "missing_consent", provider: "grok", plan: "plan", handle: "handle", request: "request", wantError: true},
		{name: "missing_request", provider: "grok", plan: "plan", handle: "handle", write: true, wantError: true},
		{name: "missing_plan", provider: "grok", request: "request", write: true, wantError: true},
		{name: "missing_handle", provider: "grok", plan: "plan", request: "request", write: true, wantError: true},
		{name: "native_args", provider: "grok", plan: "plan", handle: "handle", request: "request", write: true, args: []string{"--yolo"}, wantError: true},
		{name: "foreign_provider", provider: "claude", plan: "plan", handle: "handle", request: "request", write: true, wantError: true},
		{name: "orphan_request", provider: "agy", request: "request", wantError: true},
		{name: "unsupported_bridge", provider: "codex", plan: "plan", handle: "handle", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := controllerACPMode(tc.provider, tc.plan, tc.handle, tc.write, tc.request, tc.args)
			if got != tc.wantACP || (err != nil) != tc.wantError {
				t.Fatalf("acp=%t err=%v", got, err)
			}
		})
	}
}

func TestControllerACPMetadataHasNoHookCredentialAndRejectsForeignReplacement(t *testing.T) {
	root := privateTaskTemp(t)
	path := filepath.Join(root, "handle.acp-origin.json")
	m := controllerACPMetadata{Version: 1, TransportMode: contract.BridgeTransportGrokACP, RunID: "owned-run", OwnerMode: "local", OwnerCapabilityFile: filepath.Join(root, "owner.json"), ControlFile: filepath.Join(root, "control.json"), ManifestSHA256: contract.GrokACPManifestSHA256(), Status: "queued"}
	if err := writeControllerBridgeExclusive(path, m); err != nil {
		t.Fatal(err)
	}
	next := m
	next.BindingID = "binding"
	next.OwnerEpoch = 1
	next.Status = "bound"
	if err := replaceControllerACPMetadata(path, m, next); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{[]byte("hook_capability"), []byte("binding_file"), []byte("claim_token")} {
		if bytes.Contains(raw, forbidden) {
			t.Fatal("ACP metadata contains private protocol/hook authority")
		}
	}
	if err = replaceControllerACPMetadata(path, m, next); err == nil {
		t.Fatal("stale metadata overwrite accepted")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(root, "foreign.json")
	if err = writeExclusiveJSON(foreign, next); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(foreign, path); err != nil {
		t.Fatal(err)
	}
	if err = replaceControllerACPMetadata(path, next, m); err == nil {
		t.Fatal("metadata symlink accepted")
	}
	after, err := os.ReadFile(foreign)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("foreign metadata changed")
	}
}

func TestControllerACPExplicitFlagsReadPrivateRequestBeforeRuntime(t *testing.T) {
	root := privateTaskTemp(t)
	state := filepath.Join(root, "absent-state")
	err := controllerEntry(context.Background(), []string{"start", "--provider", "grok", "--state-dir", state, "--bridge-plan", filepath.Join(root, "plan.json"), "--bridge-handle", filepath.Join(root, "handle.json"), "--grok-session-write", "--grok-acp-request", filepath.Join(root, "absent-request.json")})
	if err == nil || err.Error() != "grokorigin_invalid_request" {
		t.Fatalf("public explicit ACP request was not validated first: %v", err)
	}
	if _, err = os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("runtime allocated before validating private ACP input")
	}
}
