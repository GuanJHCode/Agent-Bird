package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
)

func exerciseIndexedCandidateDelivery(t *testing.T, root, repo, base string, source contract.LaunchCommand, result contract.Result, git func(string, ...string) string) {
	t.Helper()
	target := filepath.Join(root, "indexed-target")
	git(repo, "worktree", "add", "-b", "indexed-delivery", target, base)
	input := func(g contract.LaunchCommand, r contract.Result) contract.AcceptedInput {
		info, err := os.Stat(r.ArtifactPath)
		if err != nil {
			t.Fatal(err)
		}
		return contract.AcceptedInput{DecisionCommandID: "accept-" + g.TaskID, AdapterPayload: g.AdapterPayload, Event: contract.Event{Version: 1, ProducerID: "producer", RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, Kind: contract.EventResult, EventID: "event-" + g.TaskID, EventRevision: 1, ActionSlot: "slot-" + g.TaskID, CommandID: g.CommandID, Sequence: 1, PayloadHash: r.OutputHash, Artifact: &contract.ArtifactRef{ID: g.TaskID, Path: r.ArtifactPath, SHA256: r.OutputHash, Size: info.Size()}}}
	}
	action := func(op, from string, ordinal int) map[string]any {
		return map[string]any{"version": 1, "operation": op, "source_task": from, "target": map[string]any{"worktree": target, "ref": "refs/heads/indexed-delivery", "base_oid": base}, "review_index": map[string]any{"version": 1, "mode": "indexed_candidate_review_v1", "shards": 3, "review_ordinal": ordinal}}
	}
	run := func(name string, payload map[string]any, accepted contract.AcceptedInput, vector *contract.DependencyInputVector) (contract.LaunchCommand, contract.Result, host.CandidateDelivery) {
		t.Helper()
		body, _ := json.Marshal(payload)
		grant := contract.LaunchCommand{RunID: source.RunID, TaskID: name, AttemptID: "attempt-" + name, SegmentID: "segment-" + name, CommandID: "command-" + name, ReservationID: "reservation-" + name, SlotToken: "slot-" + name, WorkRevision: 1, PlanRevision: 1, GrantedActiveMS: 15000, AdapterPayload: body, Input: &accepted, DependencyInputs: vector}
		var err error
		grant.LaunchIntentHash, err = contract.CandidateIntentHash(grant)
		if err != nil {
			t.Fatal(err)
		}
		inv, err := invocationForGrant(context.Background(), grant)
		if err != nil {
			t.Fatalf("%s invocation: %v", name, err)
		}
		h, err := host.NewIPC(filepath.Join(root, "state", "host-spool", "indexed-"+name), "producer")
		if err != nil {
			t.Fatal(err)
		}
		out, err := h.ExecuteLaunch(context.Background(), grant, inv)
		raw, readErr := os.ReadFile(out.ArtifactPath)
		if err != nil || out.Status != "result_ready" || readErr != nil {
			t.Fatalf("%s status=%s err=%v read=%v artifact=%s", name, out.Status, err, readErr, raw)
		}
		var delivery host.CandidateDelivery
		if err = json.Unmarshal(raw, &delivery); err != nil {
			t.Fatal(err)
		}
		return grant, out, delivery
	}
	validate := action("validate", source.TaskID, 0)
	validate["command"] = []string{"/usr/bin/grep", "-q", "worker implementation", "README"}
	checked, checkResult, checkedDelivery := run("validate", map[string]any{"kind": "candidate", "directory": filepath.Join(root, "indexed-validation"), "candidate_action": validate}, input(source, result), nil)
	if checkedDelivery.ReviewIndex == nil || len(checkedDelivery.ReviewIndex.Chunks) < 130 {
		t.Fatal("large complete candidate lost context")
	}
	manifest := gitops.ReviewIndexManifestSHA256(*checkedDelivery.ReviewIndex)
	var candidateSource map[string]any
	if err := json.Unmarshal(source.AdapterPayload, &candidateSource); err != nil {
		t.Fatal(err)
	}
	lock := candidateSource["provider_lock"].(map[string]any)
	lock["provider"], lock["protocol"] = "grok-build", "grok-streaming-json-v1"
	lock["binary"].(map[string]any)["version"] = "grok 1.0.34 (3736acbc8658)"
	home := filepath.Join(root, "grok-indexed-home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_HOME", home)
	t.Setenv("GROK_REVIEW_FIXTURE", "1")
	t.Setenv("GROK_INDEXED_FIXTURE", "1")
	t.Setenv("CANDIDATE_FIXTURE_MODE", "review_approve")
	vector := &contract.DependencyInputVector{Version: 1}
	covered := map[int]bool{}
	for ordinal := 0; ordinal < 3; ordinal++ {
		name := fmt.Sprintf("review-%d", ordinal)
		payload := map[string]any{"provider": "grok-build", "provider_lock": lock, "profile": map[string]any{"version": 1, "role": "reviewer", "permission": "read-only", "timeout_ms": 15000, "grok_session_write": true}, "directory": filepath.Join(root, name), "prompt": "Review the explicitly assigned immutable chunks", "candidate_action": action("review", "validate", ordinal)}
		grant, result, delivery := run(name, payload, input(checked, checkResult), nil)
		if delivery.ReviewIndex == nil || gitops.ReviewIndexManifestSHA256(*delivery.ReviewIndex) != manifest {
			t.Fatal("shard rebuilt or substituted accepted manifest")
		}
		prompt, err := os.ReadFile(filepath.Join(root, "state", "host-spool", "indexed-"+name, grant.AttemptID, grant.SegmentID, "scratch", "review-input.txt"))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(prompt)
		if len(prompt) > 384*1024 || delivery.ReviewInputSHA256 != hex.EncodeToString(digest[:]) || delivery.Review.Summary != "consumed-prompt-sha256:"+delivery.ReviewInputSHA256 {
			t.Fatal("actual consumer did not attest the exact bounded prompt bytes")
		}
		_, raw, ok := strings.Cut(string(prompt), "Host snapshot JSON:\n")
		if !ok {
			t.Fatal("missing prompt payload")
		}
		var supplied struct {
			Chunks []struct {
				Ordinal int
				Content string
			}
		}
		if err = json.Unmarshal([]byte(raw), &supplied); err != nil {
			t.Fatal(err)
		}
		for _, chunk := range supplied.Chunks {
			if chunk.Ordinal%3 != ordinal || covered[chunk.Ordinal] {
				t.Fatal("overlapping or substituted shard assignment")
			}
			covered[chunk.Ordinal] = true
		}
		vector.Inputs = append(vector.Inputs, input(grant, result))
	}
	if len(covered) != len(checkedDelivery.ReviewIndex.Chunks) {
		t.Fatal("consumer coverage is incomplete")
	}
	// Anchor the middle shard, so vector ordering cannot silently select shard0.
	integrate := action("integrate", "review-1", 1)
	integrate["dependency_input_version"] = 1
	_, _, integrated := run("integrate", map[string]any{"kind": "candidate", "directory": target, "candidate_action": integrate}, vector.Inputs[1], vector)
	if integrated.Integration == nil || integrated.Integration.State != gitops.IntegrationIntegrated || git(target, "rev-parse", "HEAD") != checkedDelivery.Candidate.CandidateOID || git(repo, "rev-parse", "HEAD") != base {
		t.Fatal("complete indexed review did not deliver exactly the frozen candidate")
	}
	if !reflect.DeepEqual(integrated.Candidate.OrderedInputOIDs, checkedDelivery.Candidate.OrderedInputOIDs) {
		t.Fatal("integration changed ordered source identities")
	}
}
