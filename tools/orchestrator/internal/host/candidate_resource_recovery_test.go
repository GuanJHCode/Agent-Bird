package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitopsworker"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestCandidateResourceRecoveryRegistersExitedProducerResult(t *testing.T) {
	for _, presealed := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing_registration", true: "sealed_before_append"}[presealed], func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			h, err := NewIPC(root, "host")
			if err != nil {
				t.Fatal(err)
			}
			grant := candidateResourceRecoveryGrant(t, h)
			if scope := candidateResourceScopeForGrant(grant); scope != "producer" {
				t.Fatalf("fixture recovery scope=%q", scope)
			}
			a := store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID}
			meta := metadataForLaunch(grant)
			meta.candidateResourceKind = "producer"
			body := candidateResourceRecoveryArtifact(t, root, grant)
			source, ok := h.candidateResourceSource(grant.RunID, grant.TaskID, a, meta, body)
			if !ok {
				t.Fatalf("fixture candidate artifact not accepted: %+v", source)
			}
			if presealed {
				if _, err = gitopsworker.SealCandidateJournal(context.Background(), root, source); err != nil {
					t.Fatal(err)
				}
			}
			artifact, err := h.writeHostArtifact(meta, "terminal", 0, "terminal", "result-output.bin", body)
			if err != nil {
				t.Fatal(err)
			}
			spool, err := h.spool(a)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = h.appendPhaseEvent(spool, grant.RunID, grant.TaskID, a, meta, contract.EventResult, artifact.SHA256, nil, nil, artifact); err != nil {
				t.Fatal(err)
			}
			if _, err = h.appendPhaseEvent(spool, grant.RunID, grant.TaskID, a, meta, contract.EventExited, hashText("exited:"+a.SegmentID), nil, nil, artifact); err != nil {
				t.Fatal(err)
			}
			binding, err := h.artifactSlot(meta, "terminal", 0)
			if err != nil {
				t.Fatal(err)
			}
			desc, proof, _, err := artifacts.LoadHostSlot(context.Background(), root, binding)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := hostArtifactReference(artifact.ID, desc, proof)
			if err != nil || !reflect.DeepEqual(loaded, artifact) {
				t.Fatalf("fixture terminal receipt mismatch loaded=%+v original=%+v err=%v", loaded, artifact, err)
			}

			restarted, err := NewIPC(root, grant.HostID)
			if err != nil {
				t.Fatal(err)
			}
			if err = restarted.recoverDuplicateGrant(grant, 9); err != nil {
				t.Fatal(err)
			}
			records, err := restarted.Collect(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != 3 || records[2].Kind != contract.EventCandidateResourceRegistered || len(records[2].CandidateResource) == 0 {
				t.Fatalf("recovery did not append candidate registration: %+v", records)
			}
			var header gitops.CandidateResourceHeader
			if err = json.Unmarshal(records[2].CandidateResource, &header); err != nil {
				t.Fatal(err)
			}
			if header.Source.Kind != "producer" || header.Source.CommandID != grant.CommandID || header.Source.Binding.RunID != grant.RunID || records[2].PayloadHash != hashBytes(records[2].CandidateResource) {
				t.Fatalf("unexpected recovery registration: %+v", records[2])
			}
			if err = os.Rename(filepath.Join(root, ".gitops-journal"), filepath.Join(root, "sealed-source-moved")); err != nil {
				t.Fatal(err)
			}
			if err = restarted.recoverDuplicateGrant(grant, 10); err != nil {
				t.Fatal(err)
			}
			again, err := restarted.Collect(context.Background(), a)
			if err != nil || len(again) != len(records) || !reflect.DeepEqual(again, records) {
				t.Fatalf("registered replay reopened source or appended again: %+v %v", again, err)
			}
		})
	}
}

func candidateResourceRecoveryGrant(t *testing.T, h *Host) contract.LaunchCommand {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"kind": "provider",
		"candidate_workspace": map[string]any{
			"version": 1, "auto_directory": true, "repo_root": "/repo", "base_oid": strings.Repeat("a", 40), "paths": []string{"x"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return managedHostGrant(t, h, contract.LaunchCommand{CommandID: "command", ReservationID: "reservation", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 3, PlanRevision: 7, GrantedActiveMS: 1000, AdapterPayload: payload})
}

func TestCandidateResourceRecoveryScopeExcludesIneligibleProducerGrants(t *testing.T) {
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "host")
	if err != nil {
		t.Fatal(err)
	}
	grant := candidateResourceRecoveryGrant(t, h)
	for _, tc := range []struct {
		name   string
		mutate func(*contract.LaunchCommand)
	}{
		{"continuation", func(g *contract.LaunchCommand) { g.Continuation = &contract.ContinuationRef{} }},
		{"rework", func(g *contract.LaunchCommand) {
			g.AdapterPayload = []byte(strings.Replace(string(g.AdapterPayload), `"paths":["x"]`, `"paths":["x"],"rework":{}`, 1))
		}},
		{"new_base_rework", func(g *contract.LaunchCommand) {
			g.AdapterPayload = []byte(strings.Replace(string(g.AdapterPayload), `"paths":["x"]`, `"paths":["x"],"new_base_rework":{}`, 1))
		}},
		{"filter", func(g *contract.LaunchCommand) { g.GitFilter = &contract.GitFilterResourceGrant{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := grant
			tc.mutate(&got)
			if scope := candidateResourceScopeForGrant(got); scope != "" {
				t.Fatalf("ineligible grant recovered scope %q", scope)
			}
		})
	}
}

func TestCandidateResourceRecoveryScopeIncludesV2Compose(t *testing.T) {
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "host")
	if err != nil {
		t.Fatal(err)
	}
	grant := candidateResourceRecoveryGrant(t, h)
	grant.AdapterPayload = []byte(`{"candidate_action":{"version":2,"dependency_input_version":2,"operation":"compose","auto_directory":true}}`)
	if scope := candidateResourceScopeForGrant(grant); scope != "compose" {
		t.Fatalf("compose scope=%q", scope)
	}
}

func candidateResourceRecoveryArtifact(t *testing.T, root string, grant contract.LaunchCommand) []byte {
	t.Helper()
	binding := gitops.CandidateBinding{RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: grant.WorkRevision, PlanRevision: grant.PlanRevision}
	key := hashText(binding.RunID + "\x00" + binding.TaskID + "\x00" + binding.AttemptID + "\x00" + binding.SegmentID)
	journal, err := gitopsworker.OpenJournal(filepath.Join(root, ".gitops-journal", key))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	identity := gitops.DirectoryIdentity{Device: 1, Inode: 2, Mode: 040700, UID: 0}
	receipt := gitops.CandidateReceipt{Binding: &binding, AttemptID: binding.AttemptID, RepoRoot: "/repo", Worktree: "/work", CommonDir: "/common", BaseOID: strings.Repeat("1", 40), CandidateOID: strings.Repeat("2", 40), TreeOID: strings.Repeat("3", 40), RootIdentity: identity, WorktreeIdentity: "/work", RepoRootIdentity: identity, CommonDirIdentity: identity, Files: map[string]gitops.FileIdentity{"x": {Inode: 1}}, PlanRevision: int64(binding.PlanRevision), PrivateRef: "refs/orchestrator/g3/candidate-" + key, PrivateRefExpectedOID: strings.Repeat("2", 40)}
	proc := &process.Identity{PID: 41, PGID: 41, Birth: "birth", BirthKnown: true, Executable: "/usr/bin/git", ExecutableSHA256: strings.Repeat("b", 64), StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	for _, phase := range []string{"intent", "spawned", "exited"} {
		record := gitops.GitProcessRecord{Phase: phase, CommandDigest: strings.Repeat("c", 64)}
		if phase != "intent" {
			record.Process = proc
		}
		if err = journal.RecordGitProcess(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	if err = journal.RecordCandidateFreeze(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	publicReceipt := receipt
	publicReceipt.Files = nil
	raw, err := json.Marshal(struct {
		Version        int                     `json:"version"`
		ReceiptSHA256  string                  `json:"candidate_receipt_sha256"`
		ProviderResult string                  `json:"provider_result"`
		Candidate      gitops.CandidateReceipt `json:"candidate"`
	}{Version: 1, ReceiptSHA256: gitops.CandidateResourceReceiptDigest(receipt), ProviderResult: "complete", Candidate: publicReceipt})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
