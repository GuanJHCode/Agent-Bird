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

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
)

// Actual managed producers, original Host worker Wait, Git composition and v1
// consumers. Acceptance tuples are controlled fixtures; Store is tested separately.
func TestOrderedComposeHostDeliveryChain(t *testing.T) {
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
	git(repo, "config", "user.name", "Compose Test")
	git(repo, "config", "user.email", "compose@example.invalid")
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
	binary, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(executable)
	lock := map[string]any{"version": 1, "provider": "claude-code", "protocol": "claude-stream-json-v1", "binary": map[string]string{"path": binary, "version": "fixture-v1", "sha256": hex.EncodeToString(sum[:])}}
	runID := "ordered-" + hex.EncodeToString(sum[:8]) + filepath.Base(root)
	launch := func(name string, payload map[string]any, input *contract.AcceptedInput, vector *contract.DependencyInputVector) (contract.LaunchCommand, contract.Result, error) {
		t.Helper()
		body, _ := json.Marshal(payload)
		g := contract.LaunchCommand{RunID: runID, TaskID: name, AttemptID: "attempt-" + name, SegmentID: "segment-" + name, CommandID: "command-" + name, ReservationID: "reservation-" + name, WorkRevision: 1, PlanRevision: 1, GrantedActiveMS: 30000, AdapterPayload: body, Input: input, DependencyInputs: vector}
		g = candidateHostGrant(t, g, "host-"+name)
		g.LaunchIntentHash, err = contract.CandidateIntentHash(g)
		if err != nil {
			t.Fatal(err)
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
			b, _ := os.ReadFile(r.ArtifactPath)
			t.Fatalf("%s status=%s err=%v body=%s", name, r.Status, e, b)
		}
		return g, r
	}
	decode := func(r contract.Result) host.CandidateDelivery {
		t.Helper()
		var d host.CandidateDelivery
		b, e := os.ReadFile(r.ArtifactPath)
		if e != nil || json.Unmarshal(b, &d) != nil {
			t.Fatalf("decode: %v %s", e, b)
		}
		return d
	}
	inputs := map[string]contract.AcceptedInput{}
	oids := map[string]string{}
	producerRefs := map[string]string{}
	for _, name := range []string{"A", "B"} {
		t.Setenv("CANDIDATE_FIXTURE_MODE", "compose-"+strings.ToLower(name))
		g, r := ready(name, map[string]any{"provider": "claude-code", "provider_lock": lock, "directory": filepath.Join(root, "producer-"+name), "prompt": "controlled implementation", "profile": map[string]any{"version": 1, "role": "implementer", "permission": "workspace-write", "timeout_ms": 30000}, "candidate_workspace": map[string]any{"version": 1, "repo_root": repo, "base_oid": base, "paths": []string{strings.ToLower(name) + ".txt"}}}, nil, nil)
		inputs[name] = candidateAcceptedInput(t, root, g, r)
		oids[name] = decode(r).Candidate.CandidateOID
		producerRefs[name] = decode(r).Candidate.PrivateRef
	}
	vector := &contract.DependencyInputVector{Version: 2, Inputs: []contract.AcceptedInput{inputs["B"], inputs["A"]}}
	composePayload := map[string]any{"kind": "candidate", "directory": filepath.Join(root, "compose"), "candidate_action": map[string]any{"version": 2, "operation": "compose", "dependency_input_version": 2, "ordered_source_tasks": []string{"B", "A"}}}
	// Ref retargeting must stop before creating any compose worktree or pin.
	git(repo, "update-ref", producerRefs["B"], base, oids["B"])
	beforeWorktrees := git(repo, "worktree", "list", "--porcelain")
	_, rejectedSource, sourceErr := launch("source-drift", composePayload, nil, vector)
	if rejectedSource.Status != "failed" {
		t.Fatalf("drifted producer pin was not rejected: %+v %v", rejectedSource, sourceErr)
	}
	if git(repo, "worktree", "list", "--porcelain") != beforeWorktrees || git(repo, "for-each-ref", "--format=%(refname)", "refs/orchestrator/g3/compose-") != "" {
		t.Fatal("rejected producer changed Git state")
	}
	git(repo, "update-ref", producerRefs["B"], oids["B"], base)
	composed, composedResult := ready("compose", composePayload, nil, vector)
	d := decode(composedResult)
	want := []string{oids["B"], oids["A"]}
	if d.Stage != "compose" || d.Composition == nil || d.Candidate.Binding == nil || d.Candidate.Binding.TaskID != "compose" || !reflect.DeepEqual(d.Candidate.OrderedInputOIDs, want) || !reflect.DeepEqual(d.Composition.OrderedInputOIDs, want) || d.Composition.Sources[0].TaskID != "B" || d.ProviderLock != nil {
		t.Fatalf("composition binding/order lost: %+v", d)
	}
	if git(repo, "rev-parse", d.Candidate.PrivateRef) != d.Candidate.CandidateOID || git(repo, "show", d.Candidate.CandidateOID+":a.txt") != "compose-a" || git(repo, "show", d.Candidate.CandidateOID+":b.txt") != "compose-b" {
		t.Fatal("wrong merged tree")
	}
	// Each inner Git process must retain an original identity and terminal pair.
	key := sha256.Sum256([]byte(composed.CommandID))
	journal := filepath.Join(root, "state", "host-spool", composed.HostID, "gitops-journal", hex.EncodeToString(key[:]), "events.jsonl")
	rows, e := os.ReadFile(journal)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(rows), `"kind":"git_process"`) || !strings.Contains(string(rows), `"phase":"spawned"`) || !strings.Contains(string(rows), `"phase":"exited"`) {
		t.Fatal("compose bypassed tracked Git process ledger")
	}
	spool, e := events.Open(filepath.Join(root, "state", "host-spool", composed.HostID, composed.AttemptID, composed.SegmentID))
	if e != nil {
		t.Fatal(e)
	}
	evs, e := spool.Read()
	if e != nil {
		t.Fatal(e)
	}
	exited := false
	for _, event := range evs {
		if event.Kind == contract.EventExited {
			exited = true
		}
	}
	if !exited {
		t.Fatal("compose Host omitted original Exited event")
	}
	target := filepath.Join(root, "target")
	git(repo, "worktree", "add", "-b", "delivery", target, base)
	action := func(op, source string) map[string]any {
		return map[string]any{"version": 1, "operation": op, "source_task": source, "target": map[string]any{"worktree": target, "ref": "refs/heads/delivery", "base_oid": base}}
	}
	accepted := candidateAcceptedInput(t, root, composed, composedResult)
	check := action("validate", "compose")
	check["command"] = []string{"/usr/bin/grep", "-q", "compose-a", "a.txt"}
	// A deleted composition pin must fail before validation creates a workspace.
	git(repo, "update-ref", "-d", d.Candidate.PrivateRef, d.Candidate.CandidateOID)
	rejectedPath := filepath.Join(root, "deleted-ref-validation")
	_, bad, badErr := launch("deleted-ref-validation", map[string]any{"kind": "candidate", "directory": rejectedPath, "candidate_action": check}, &accepted, nil)
	if bad.Status != "failed" {
		t.Fatalf("deleted compose pin was not rejected: %+v %v", bad, badErr)
	}
	if _, e = os.Lstat(rejectedPath); !os.IsNotExist(e) {
		t.Fatal("deleted pin created validation workspace", e)
	}
	git(repo, "update-ref", d.Candidate.PrivateRef, d.Candidate.CandidateOID)
	checked, checkedResult := ready("validate", map[string]any{"kind": "candidate", "directory": filepath.Join(root, "validation"), "candidate_action": check}, &accepted, nil)
	accepted = candidateAcceptedInput(t, root, checked, checkedResult)
	t.Setenv("CANDIDATE_FIXTURE_MODE", "review_approve")
	reviewed, reviewResult := ready("review", map[string]any{"provider": "claude-code", "provider_lock": lock, "directory": filepath.Join(root, "review"), "prompt": "controlled review", "profile": map[string]any{"version": 1, "role": "reviewer", "permission": "read-only", "timeout_ms": 30000}, "candidate_action": action("review", "validate")}, &accepted, nil)
	accepted = candidateAcceptedInput(t, root, reviewed, reviewResult)
	_, integratedResult := ready("integrate", map[string]any{"kind": "candidate", "directory": target, "candidate_action": action("integrate", "review")}, &accepted, nil)
	for _, r := range []contract.Result{checkedResult, reviewResult, integratedResult} {
		got := decode(r)
		if !reflect.DeepEqual(got.Composition, d.Composition) || got.CandidateReceiptSHA256 != d.CandidateReceiptSHA256 {
			t.Fatal("downstream lost composition provenance")
		}
	}
	if decode(reviewResult).ProviderLock == nil {
		t.Fatal("review lost its own provider lock")
	}
	if git(target, "rev-parse", "HEAD") != d.Candidate.CandidateOID || git(repo, "rev-parse", "HEAD") != base || git(repo, "status", "--porcelain=v1") != before {
		t.Fatal("integration changed the wrong version or user work")
	}
	b, e := os.ReadFile(filepath.Join(repo, "README"))
	if e != nil || string(b) != "user unfinished work\n" {
		t.Fatal("user bytes changed")
	}
}
