package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
)

// The adapter package's probe_policy_test covers the trusted command seam that
// classifies an unstarted EAGAIN/EINTR. This fixture deliberately fails any
// dynamic probe: task submit must leave it for the admitted Host, where
// launch_failure_policy_test proves the typed fact is durably retried.
func retryPolicyPreflightTask(t *testing.T, root string) (coordinator.TaskRequest, string) {
	t.Helper()
	marker := filepath.Join(root, "dynamic-probe-ran")
	body := []byte("#!/bin/sh\n: > '" + marker + "'\nexit 75\n")
	binary := filepath.Join(root, "provider")
	if err := os.WriteFile(binary, body, 0700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	pin := adapter.BinaryPin{Path: binary, Version: "fixture-v1", SHA256: hex.EncodeToString(digest[:])}
	payload, err := json.Marshal(adapter.InvocationPayload{
		Provider: string(adapter.ProviderClaude), Directory: root, Prompt: "review",
		ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: pin},
		Profile:      &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, CLIModelDefault: true, TimeoutMS: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := &contract.ExecutionPolicy{Version: 1, GroupMaxAttempts: 2, GroupMaxActiveMS: 2000, TaskMaxActiveMS: 1000, SegmentMaxActiveMS: 1000, Retry: &contract.RetryPolicy{Codes: []string{"probe_start_eagain"}, BackoffMS: []int64{1}}}
	return coordinator.TaskRequest{ID: "retry", MaxAttempts: 2, MaxActiveMS: 1000, ExecutionPolicy: policy, AdapterPayload: payload}, marker
}

func TestTaskSubmitRetryPolicyDefersDynamicProbeUntilHostAdmission(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "")
	root := privateTaskTemp(t)
	task, marker := retryPolicyPreflightTask(t, root)
	plan := taskPlan{RunID: "retry-run", PlanRevision: 1, Tasks: []coordinator.TaskRequest{task}}
	request := filepath.Join(root, "request.json")
	if err := writeExclusiveJSON(request, plan); err != nil {
		t.Fatal(err)
	}
	err := taskSubmit(context.Background(), "submit", []string{"--request", request, "--handle", filepath.Join(root, "handle.json"), "--state-dir", filepath.Join(root, "state")}, &bytes.Buffer{})
	if err == nil || err.Error() != "current_codex_thread_unavailable" {
		t.Fatalf("submit reached dynamic provider preflight: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("dynamic probe ran before Host admission: %v", err)
	}
}

func TestPreflightRetryPolicyRejectsUnsupportedFallbacks(t *testing.T) {
	for name, raw := range map[string]string{
		"candidate": `{"kind":"candidate","candidate_action":{"version":1,"operation":"validate","source_task":"source","command":["/usr/bin/true"]}}`,
		"review":    `{"provider":"claude-code","candidate_action":{"version":1,"operation":"review","source_task":"source"}}`,
		"gitops":    `{"kind":"gitops"}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := privateTaskTemp(t)
			task, marker := retryPolicyPreflightTask(t, root)
			task.Fallbacks = []json.RawMessage{json.RawMessage(raw)}
			if err := preflightProviders(context.Background(), []coordinator.TaskRequest{task}); err == nil || err.Error() != "execution_policy_retry_stage_unsupported" {
				t.Fatalf("unsupported fallback preflight: %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("unsupported fallback reached dynamic probe")
			}
		})
	}
}

func TestPreflightProvidersRetryPolicyRetainsStaticGuardsAndLegacyDynamicProbe(t *testing.T) {
	root := privateTaskTemp(t)
	task, marker := retryPolicyPreflightTask(t, root)
	if err := preflightProviders(context.Background(), []coordinator.TaskRequest{task}); err != nil {
		t.Fatalf("retry policy rejected before Host admission: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("retry policy ran a dynamic probe")
	}

	badPolicy := task
	badPolicy.ExecutionPolicy = &contract.ExecutionPolicy{Version: 2}
	if err := preflightProviders(context.Background(), []coordinator.TaskRequest{badPolicy}); err == nil || err.Error() != "execution_policy_invalid" {
		t.Fatalf("invalid policy skipped static validation: %v", err)
	}

	badPin := task
	payload, err := adapter.DecodeInvocationPayload(badPin.AdapterPayload)
	if err != nil {
		t.Fatal(err)
	}
	payload.ProviderLock.Binary.SHA256 = strings.Repeat("b", 64)
	badPin.AdapterPayload, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err = preflightProviders(context.Background(), []coordinator.TaskRequest{badPin}); err == nil || err.Error() != "binary_sha256_mismatch" {
		t.Fatalf("bad pinned digest was not rejected statically: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("bad static pin reached dynamic probe")
	}
	for name, raw := range map[string]json.RawMessage{
		"candidate": json.RawMessage(`{"kind":"candidate","candidate_action":{"version":1,"operation":"validate","source_task":"source","command":["/usr/bin/true"]}}`),
		"gitops":    json.RawMessage(`{"kind":"gitops"}`),
	} {
		t.Run(name, func(t *testing.T) {
			unsupported := task
			unsupported.AdapterPayload = raw
			if err := preflightProviders(context.Background(), []coordinator.TaskRequest{unsupported}); err == nil || err.Error() != "execution_policy_retry_stage_unsupported" {
				t.Fatalf("retry policy was accepted for %s: %v", name, err)
			}
		})
	}

	legacy := task
	legacy.ExecutionPolicy = nil
	if err = preflightProviders(context.Background(), []coordinator.TaskRequest{legacy}); err == nil || err.Error() != "provider_probe_failed" {
		t.Fatalf("legacy task no longer runs dynamic preflight: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("legacy dynamic probe did not run: %v", err)
	}
}

func TestPreliminaryTaskPlanKeepsOriginalNestedRequest(t *testing.T) {
	root := privateTaskTemp(t)
	realDirectory := filepath.Join(root, "workspace")
	if err := os.Mkdir(realDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(realDirectory, alias); err != nil {
		t.Fatal(err)
	}
	auto, err := json.Marshal(adapter.InvocationPayload{Kind: "candidate", CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "validate", SourceTask: "source", Command: []string{"/usr/bin/true"}}})
	if err != nil {
		t.Fatal(err)
	}
	// This exact Grok profile fails before VerifyExecutable/Probe, so no process
	// is started. Workspace normalization happens first and must stay in a copy.
	profile := adapter.InvocationPayload{Provider: string(adapter.ProviderGrok), Directory: alias, Prompt: "readonly", ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderGrok, Protocol: adapter.ProtocolID(adapter.ProviderGrok), Binary: adapter.BinaryPin{Path: filepath.Join(root, "never-executed"), Version: "grok 1.0.34 (3736acbc8658)", SHA256: strings.Repeat("a", 64)}}, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, CLIModelDefault: true, TimeoutMS: 1000}}
	primary, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	plan := taskPlan{RunID: "original", PlanRevision: 1, Tasks: []coordinator.TaskRequest{{ID: "auto", MaxAttempts: 1, MaxActiveMS: 1000, AdapterPayload: auto, Fallbacks: []json.RawMessage{auto}}, {ID: "profile", MaxAttempts: 1, MaxActiveMS: 1000, AdapterPayload: primary, Fallbacks: []json.RawMessage{fallback}}}}
	before, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var original taskPlan
	if err := json.Unmarshal(before, &original); err != nil {
		t.Fatal(err)
	}
	// JSON marshaling compacts RawMessage whitespace. Restore independent byte
	// copies so this snapshot also checks the exact primary/fallback input bytes.
	for i, task := range plan.Tasks {
		original.Tasks[i].AdapterPayload = append(json.RawMessage(nil), task.AdapterPayload...)
		for j, raw := range task.Fallbacks {
			original.Tasks[i].Fallbacks[j] = append(json.RawMessage(nil), raw...)
		}
	}
	handle := filepath.Join(root, "handle.json")
	if err := preflightTaskPlanBeforeOwner(context.Background(), handle, plan); err == nil || err.Error() != "grok_session_write_required" {
		t.Fatal("preliminary validation reached a different failure point", err)
	}
	if !reflect.DeepEqual(plan, original) {
		t.Fatal("preliminary normalization/planned workspace changed the original request or fallback")
	}
	for _, name := range []string{"handle.json", "state", "owners", "install"} {
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatal("preliminary check allocated runtime", name, err)
		}
	}
}

func TestPreliminarySupportedTaskStillRequiresOriginalOwner(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "")
	root := privateTaskTemp(t)
	raw, err := json.Marshal(adapter.InvocationPayload{Kind: "candidate", CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "validate", SourceTask: "source", Command: []string{"/usr/bin/true"}}})
	if err != nil {
		t.Fatal(err)
	}
	plan := taskPlan{RunID: "supported", PlanRevision: 1, Tasks: []coordinator.TaskRequest{{ID: "validate", MaxAttempts: 1, MaxActiveMS: 1000, AdapterPayload: raw}}}
	handle, state := filepath.Join(root, "handle.json"), filepath.Join(root, "state")
	if err := preflightTaskPlanBeforeOwner(context.Background(), handle, plan); err != nil {
		t.Fatal("valid preliminary plan rejected", err)
	}
	request := filepath.Join(root, "request.json")
	if err := writeExclusiveJSON(request, plan); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(request)
	if err != nil {
		t.Fatal(err)
	}
	err = taskSubmit(context.Background(), "submit", []string{"--request", request, "--handle", handle, "--state-dir", state}, &bytes.Buffer{})
	if err == nil || err.Error() != "current_codex_thread_unavailable" {
		t.Fatal("preliminary validation bypassed original owner", err)
	}
	after, err := os.ReadFile(request)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("preliminary validation rewrote original request", err)
	}
	for _, name := range []string{handle, state} {
		if _, err := os.Lstat(name); !os.IsNotExist(err) {
			t.Fatal("preliminary validation allocated runtime", name, err)
		}
	}
}
