package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
)

// This accepts only the controlled protocol boundary: A is Claude and B is
// AGY. It deliberately does not claim native-provider or account acceptance.
func TestOrderedComposeCrossProviderHostDeliveryChain(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	if err = os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		out, e := exec.Command("/usr/bin/git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %v %s", args, e, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "init", "-q", "-b", "main")
	git(repo, "config", "user.name", "Cross Provider Test")
	git(repo, "config", "user.email", "cross-provider@example.invalid")
	if err = os.WriteFile(filepath.Join(repo, "README"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", ".")
	git(repo, "commit", "-qm", "base")
	base := git(repo, "rev-parse", "HEAD")
	if err = os.WriteFile(filepath.Join(repo, "README"), []byte("user unfinished work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := git(repo, "status", "--porcelain=v1")
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	fixture, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(fixture)
	lock := func(name, provider, protocol, version string) map[string]any {
		t.Helper()
		path := filepath.Join(root, name)
		if e := os.WriteFile(path, fixture, 0700); e != nil {
			t.Fatal(e)
		}
		return map[string]any{"version": 1, "provider": provider, "protocol": protocol, "binary": map[string]string{"path": path, "version": version, "sha256": hex.EncodeToString(sum[:])}}
	}
	claudeLock := lock("compose-cross-claude-fixture", "claude-code", "claude-stream-json-v1", "fixture-v1")
	agyLock := lock("compose-cross-agy-fixture", "antigravity-cli", "agy-stream-json-v1", "fixture-v1")
	reviewLock := lock("compose-cross-review-claude-fixture", "claude-code", "claude-stream-json-v1", "fixture-v1")
	runID := "cross-provider-" + hex.EncodeToString(sum[:8]) + filepath.Base(root)
	launch := func(name string, payload map[string]any, input *contract.AcceptedInput, vector *contract.DependencyInputVector) (contract.LaunchCommand, contract.Result, error) {
		t.Helper()
		body, e := json.Marshal(payload)
		if e != nil {
			t.Fatal(e)
		}
		g := contract.LaunchCommand{RunID: runID, TaskID: name, AttemptID: "attempt-" + name, SegmentID: "segment-" + name, CommandID: "command-" + name, ReservationID: "reservation-" + name, WorkRevision: 1, PlanRevision: 1, GrantedActiveMS: 30000, AdapterPayload: body, Input: input, DependencyInputs: vector}
		g = candidateHostGrant(t, g, "cross-host-"+name)
		g.LaunchIntentHash, e = contract.CandidateIntentHash(g)
		if e != nil {
			t.Fatal(e)
		}
		inv, e := invocationForGrant(context.Background(), g, filepath.Join(root, "state", "host-spool"))
		if e != nil {
			return g, contract.Result{}, e
		}
		h, e := host.NewIPC(filepath.Join(root, "state", "host-spool", g.HostID), g.HostID)
		if e != nil {
			t.Fatal(e)
		}
		r, e := h.ExecuteLaunch(context.Background(), g, inv)
		return g, r, e
	}
	ready := func(name string, payload map[string]any, input *contract.AcceptedInput, vector *contract.DependencyInputVector) (contract.LaunchCommand, contract.Result) {
		t.Helper()
		g, r, e := launch(name, payload, input, vector)
		if e != nil || r.Status != "result_ready" {
			body, _ := os.ReadFile(r.ArtifactPath)
			paths, _ := filepath.Glob(filepath.Join(root, "state", "host-spool", g.HostID, g.AttemptID, g.SegmentID, "event-*.json"))
			rows := make([]string, 0, len(paths))
			for _, path := range paths {
				row, _ := os.ReadFile(path)
				rows = append(rows, string(row))
			}
			t.Fatalf("%s result=%+v err=%v body=%s events=%s", name, r, e, body, strings.Join(rows, ""))
		}
		return g, r
	}
	decode := func(r contract.Result) host.CandidateDelivery {
		t.Helper()
		var d host.CandidateDelivery
		body, e := os.ReadFile(r.ArtifactPath)
		if e != nil || json.Unmarshal(body, &d) != nil {
			t.Fatalf("decode: %v %s", e, body)
		}
		return d
	}
	producer := func(name, provider string, providerLock map[string]any) (contract.LaunchCommand, contract.Result) {
		profile := map[string]any{"version": 1, "role": "implementer", "permission": "workspace-write", "timeout_ms": 30000}
		return ready(name, map[string]any{"provider": provider, "provider_lock": providerLock, "directory": filepath.Join(root, "producer-"+name), "prompt": "controlled implementation", "profile": profile, "candidate_workspace": map[string]any{"version": 1, "repo_root": repo, "base_oid": base, "paths": []string{strings.ToLower(name) + ".txt"}}}, nil, nil)
	}
	claudeGrant, claudeResult := producer("A", "claude-code", claudeLock)
	agyGrant, agyResult := producer("B", "antigravity-cli", agyLock)
	inputs := map[string]contract.AcceptedInput{
		"A": candidateAcceptedInput(t, root, claudeGrant, claudeResult),
		"B": candidateAcceptedInput(t, root, agyGrant, agyResult),
	}
	claudeDelivery, agyDelivery := decode(claudeResult), decode(agyResult)
	vector := &contract.DependencyInputVector{Version: 2, Inputs: []contract.AcceptedInput{inputs["B"], inputs["A"]}}
	composePayload := map[string]any{"kind": "candidate", "directory": filepath.Join(root, "compose"), "candidate_action": map[string]any{"version": 2, "operation": "compose", "dependency_input_version": 2, "ordered_source_tasks": []string{"B", "A"}}}
	composed, composedResult := ready("compose", composePayload, nil, vector)
	composition := decode(composedResult)
	wantOIDs := []string{agyDelivery.Candidate.CandidateOID, claudeDelivery.Candidate.CandidateOID}
	if composition.Composition == nil || !reflect.DeepEqual(composition.Composition.OrderedInputOIDs, wantOIDs) || len(composition.Composition.Sources) != 2 || composition.ProviderLock != nil {
		t.Fatalf("cross-provider composition provenance lost: %+v", composition)
	}
	lockDigest := func(in contract.AcceptedInput) string {
		t.Helper()
		payload, e := adapter.DecodeInvocationPayload(in.AdapterPayload)
		if e != nil || payload.ProviderLock == nil {
			t.Fatalf("accepted provider lock missing: %v", e)
		}
		body, e := json.Marshal(payload.ProviderLock)
		if e != nil {
			t.Fatal(e)
		}
		digest := sha256.Sum256(body)
		return hex.EncodeToString(digest[:])
	}
	payloadDigest := func(raw []byte) string {
		digest := sha256.Sum256(raw)
		return hex.EncodeToString(digest[:])
	}
	for i, want := range []struct {
		task     string
		provider string
		in       contract.AcceptedInput
	}{{"B", "antigravity-cli", inputs["B"]}, {"A", "claude-code", inputs["A"]}} {
		source := composition.Composition.Sources[i]
		payload, err := adapter.DecodeInvocationPayload(want.in.AdapterPayload)
		if err != nil || payload.Provider != want.provider || source.TaskID != want.task || source.ProviderLockSHA256 != lockDigest(want.in) || source.AdapterPayloadSHA256 != payloadDigest(want.in.AdapterPayload) {
			t.Fatalf("source %d did not preserve its own provider lock and payload: %+v", i, source)
		}
	}
	if composition.Composition.Sources[0].ProviderLockSHA256 == composition.Composition.Sources[1].ProviderLockSHA256 {
		t.Fatal("cross-provider sources unexpectedly share a provider lock")
	}
	if git(repo, "show", composition.Candidate.CandidateOID+":a.txt") != "compose-a" || git(repo, "show", composition.Candidate.CandidateOID+":b.txt") != "compose-b" {
		t.Fatal("cross-provider compose tree mismatch")
	}
	target := filepath.Join(root, "target")
	git(repo, "worktree", "add", "-b", "delivery", target, base)
	action := func(op, source string) map[string]any {
		return map[string]any{"version": 1, "operation": op, "source_task": source, "target": map[string]any{"worktree": target, "ref": "refs/heads/delivery", "base_oid": base}}
	}
	accepted := candidateAcceptedInput(t, root, composed, composedResult)
	check := action("validate", "compose")
	check["command"] = []string{"/usr/bin/grep", "-q", "compose-b", "b.txt"}
	checked, checkedResult := ready("validate", map[string]any{"kind": "candidate", "directory": filepath.Join(root, "validation"), "candidate_action": check}, &accepted, nil)
	accepted = candidateAcceptedInput(t, root, checked, checkedResult)
	reviewProfile := map[string]any{"version": 1, "role": "reviewer", "permission": "read-only", "timeout_ms": 30000}
	reviewed, reviewResult := ready("review", map[string]any{"provider": "claude-code", "provider_lock": reviewLock, "directory": filepath.Join(root, "review"), "prompt": "controlled cross-provider review", "profile": reviewProfile, "candidate_action": action("review", "validate")}, &accepted, nil)
	accepted = candidateAcceptedInput(t, root, reviewed, reviewResult)
	_, integratedResult := ready("integrate", map[string]any{"kind": "candidate", "directory": target, "candidate_action": action("integrate", "review")}, &accepted, nil)
	for _, result := range []contract.Result{checkedResult, reviewResult, integratedResult} {
		delivery := decode(result)
		if !reflect.DeepEqual(delivery.Composition, composition.Composition) || delivery.CandidateReceiptSHA256 != composition.CandidateReceiptSHA256 {
			t.Fatal("downstream delivery lost cross-provider composition provenance")
		}
	}
	review := decode(reviewResult)
	if review.ProviderLock == nil || review.ProviderLock.Provider != "claude-code" || review.ProviderLock.Binary.Path != reviewLock["binary"].(map[string]string)["path"] {
		t.Fatalf("review did not bind its own Claude lock: %+v", review.ProviderLock)
	}
	if git(target, "rev-parse", "HEAD") != composition.Candidate.CandidateOID || git(repo, "rev-parse", "HEAD") != base || git(repo, "status", "--porcelain=v1") != before {
		t.Fatal("integration changed the wrong commit or user workspace")
	}
}
