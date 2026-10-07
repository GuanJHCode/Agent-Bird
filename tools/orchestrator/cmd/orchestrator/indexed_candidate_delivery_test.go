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
	var sourceConfig struct {
		Config *contract.ConfigGenerationRef `json:"config_generation"`
	}
	if err := json.Unmarshal(source.AdapterPayload, &sourceConfig); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "indexed-target")
	git(repo, "worktree", "add", "-b", "indexed-delivery", target, base)
	input := func(g contract.LaunchCommand, r contract.Result) contract.AcceptedInput {
		return candidateAcceptedInput(t, root, g, r)
	}
	action := func(op, from string, ordinal int) map[string]any {
		return map[string]any{"version": 1, "operation": op, "source_task": from, "target": map[string]any{"worktree": target, "ref": "refs/heads/indexed-delivery", "base_oid": base}, "review_index": map[string]any{"version": 1, "mode": "indexed_candidate_review_v1", "shards": 3, "review_ordinal": ordinal}}
	}
	run := func(name string, payload map[string]any, accepted contract.AcceptedInput, vector *contract.DependencyInputVector) (contract.LaunchCommand, contract.Result, host.CandidateDelivery) {
		t.Helper()
		if sourceConfig.Config != nil {
			payload["config_generation"] = sourceConfig.Config
			payload["config_model_source"] = "not-applicable"
			if profile, ok := payload["profile"].(map[string]any); ok {
				profile["cli_model_default"] = true
				payload["config_model_source"] = "cli-default"
			}
		}
		body, _ := json.Marshal(payload)
		grant := contract.LaunchCommand{RunID: source.RunID, TaskID: name, AttemptID: "attempt-" + name, SegmentID: "segment-" + name, CommandID: "command-" + name, ReservationID: "reservation-" + name, SlotToken: "slot-" + name, WorkRevision: 1, PlanRevision: 1, GrantedActiveMS: 15000, AdapterPayload: body, Input: &accepted, DependencyInputs: vector}
		grant = candidateHostGrant(t, grant, "indexed-"+name)
		var err error
		grant.LaunchIntentHash, err = contract.CandidateIntentHash(grant)
		if err != nil {
			t.Fatal(err)
		}
		inv, err := invocationForGrant(context.Background(), grant, filepath.Join(root, "state", "host-spool"))
		if err != nil {
			t.Fatalf("%s invocation: %v", name, err)
		}
		h, err := host.NewIPC(filepath.Join(root, "state", "host-spool", grant.HostID), grant.HostID)
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
		if !reflect.DeepEqual(delivery.ConfigGeneration, sourceConfig.Config) {
			t.Fatal("stage lost selected config generation")
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
		if delivery.ReviewIndex == nil || gitops.ReviewIndexManifestSHA256(*delivery.ReviewIndex) != manifest || delivery.ReviewCoverage == nil || contract.ValidateIndexedReviewCoverage(*delivery.ReviewIndex, *delivery.ReviewCoverage) != nil || delivery.ReviewCoverage.GrantIntentSHA256 != grant.LaunchIntentHash || delivery.ReviewCoverage.AcceptedInputSHA256 != contract.IndexedReviewJSONSHA256(grant.Input) {
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
	for _, kind := range []string{"missing_shard", "missing_proof", "missing_page", "duplicate_ordinal", "assignment", "consumer_input", "source_event", "validation", "candidate_order", "config", "provider_lock"} {
		t.Run("reject_"+kind, func(t *testing.T) {
			// Exercise historical data-only body mutations, which use the same
			// semantic validator after reading. Modern positive shards above keep
			// their original immutable receipts; those objects cannot be rewritten.
			raw, _ := json.Marshal(vector)
			var badVector contract.DependencyInputVector
			if err := json.Unmarshal(raw, &badVector); err != nil {
				t.Fatal(err)
			}
			entry := &badVector.Inputs[0]
			raw, err := os.ReadFile(entry.Event.Artifact.Path)
			if err != nil {
				t.Fatal(err)
			}
			var d host.CandidateDelivery
			if err = json.Unmarshal(raw, &d); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing_proof":
				d.ReviewCoverage = nil
			case "missing_page":
				d.ReviewIndex.Pages = nil
			case "duplicate_ordinal":
				d.ReviewIndex.Chunks[1].Ordinal = 0
			case "assignment":
				d.ReviewCoverage.AssignmentSHA256 = strings.Repeat("a", 64)
			case "consumer_input":
				d.ReviewInputSHA256 = strings.Repeat("b", 64)
			case "source_event":
				d.InputEventID = "substituted"
			case "validation":
				d.Validation.CandidateOID = base
			case "candidate_order":
				d.Candidate.OrderedInputOIDs = append(d.Candidate.OrderedInputOIDs, base)
			case "config":
				d.ConfigGeneration = &contract.ConfigGenerationRef{SchemaVersion: 1, OwnerScopeSHA256: strings.Repeat("a", 64), GenerationID: strings.Repeat("f", 64), ManifestSHA256: strings.Repeat("c", 64), PackageVersion: "1.0.0"}
			case "provider_lock":
				d.ProviderLock.Binary.SHA256 = strings.Repeat("c", 64)
			}
			raw, _ = json.Marshal(d)
			artifact := filepath.Join(root, "rejected-"+kind+".json")
			if err = os.WriteFile(artifact, raw, 0600); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(raw)
			entry.Event.PayloadHash = hex.EncodeToString(digest[:])
			entry.Event.Artifact = &contract.ArtifactRef{Path: artifact, Size: int64(len(raw)), SHA256: entry.Event.PayloadHash}
			if kind == "missing_shard" {
				badVector.Inputs = badVector.Inputs[1:]
			}
			badPayload := map[string]any{"kind": "candidate", "directory": target, "candidate_action": integrate}
			if sourceConfig.Config != nil {
				badPayload["config_generation"] = sourceConfig.Config
				badPayload["config_model_source"] = "not-applicable"
			}
			payload, _ := json.Marshal(badPayload)
			grant := contract.LaunchCommand{RunID: source.RunID, TaskID: "reject-" + kind, AttemptID: "attempt-reject-" + kind, SegmentID: "segment-reject-" + kind, CommandID: "cmd-" + kind, ReservationID: "reservation-" + kind, SlotToken: "slot-" + kind, WorkRevision: 1, PlanRevision: 1, GrantedActiveMS: 15000, AdapterPayload: payload, Input: &vector.Inputs[1], DependencyInputs: &badVector}
			grant = candidateHostGrant(t, grant, "reject-"+kind)
			grant.LaunchIntentHash, err = contract.CandidateIntentHash(grant)
			if err != nil {
				t.Fatal(err)
			}
			inv, err := invocationForGrant(context.Background(), grant, filepath.Join(root, "state", "host-spool"))
			if err != nil {
				t.Fatal(err)
			}
			spool := filepath.Join(root, "state", "host-spool", "reject-"+kind)
			h, err := host.NewIPC(spool, grant.HostID)
			if err != nil {
				t.Fatal(err)
			}
			out, launchErr := h.ExecuteLaunch(context.Background(), grant, inv)
			if launchErr == nil && out.Status == "result_ready" {
				t.Fatal("invalid coverage integrated")
			}
			if launchErr == nil || launchErr.Error() != "candidate_review_coverage_changed" {
				t.Fatalf("did not reach indexed coverage validation: status=%s err=%v", out.Status, launchErr)
			}
			if _, err = os.Stat(filepath.Join(spool, ".gitops-journal")); !os.IsNotExist(err) {
				t.Fatalf("rejection created journal: %v", err)
			}
			if git(target, "rev-parse", "HEAD") != base || git(target, "status", "--porcelain") != "" {
				t.Fatal("rejection changed target")
			}
		})
		if t.Failed() {
			t.FailNow()
		}
	}
	_, _, integrated := run("integrate", map[string]any{"kind": "candidate", "directory": target, "candidate_action": integrate}, vector.Inputs[1], vector)
	if integrated.Integration == nil || integrated.Integration.State != gitops.IntegrationIntegrated || git(target, "rev-parse", "HEAD") != checkedDelivery.Candidate.CandidateOID || git(repo, "rev-parse", "HEAD") != base {
		t.Fatal("complete indexed review did not deliver exactly the frozen candidate")
	}
	if integrated.DependencyInputsSHA256 != contract.IndexedReviewJSONSHA256(vector) || !reflect.DeepEqual(integrated.Candidate.OrderedInputOIDs, checkedDelivery.Candidate.OrderedInputOIDs) {
		t.Fatal("integration changed ordered source identities")
	}
}
