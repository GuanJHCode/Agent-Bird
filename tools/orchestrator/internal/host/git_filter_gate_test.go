package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type filterGateInvocation struct {
	launchInvocation
	observed *bool
}

func (i filterGateInvocation) Args() []string {
	*i.observed = true
	return nil // Never start a process even if the closed gate regresses.
}

func TestGitFilterHostRemainsClosedBeforeSideEffects(t *testing.T) {
	for _, kind := range []string{"producer", "validator"} {
		t.Run(kind, func(t *testing.T) {
			h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "host")
			if err != nil {
				t.Fatal(err)
			}
			pin := strings.Repeat("a", 64)
			m := contract.GitFilterManifestV1{Version: 1, DriverID: "fixture", Paths: []string{"file.txt"}, Git: contract.GitFilterGitPin{Path: "/fixture/git", Version: "fixture", SHA256: pin}, Clean: contract.GitFilterToolPin{Path: "/fixture/filter", SHA256: pin, Args: []string{}}, Smudge: contract.GitFilterToolPin{Path: "/fixture/filter", SHA256: pin, Args: []string{}}, Attributes: []contract.GitFilterAttributePin{}, ConfigSHA256: pin}
			payload := map[string]any{"kind": "provider", "candidate_workspace": map[string]any{"version": 1}, "profile": map[string]any{"role": "implementer", "permission": "workspace-write"}, "git_filter": m}
			if kind == "validator" {
				payload = map[string]any{"kind": "candidate", "candidate_action": map[string]any{"version": 1, "operation": "validate"}, "git_filter": m}
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			g := withHostArtifactsForTest(t, contract.LaunchCommand{RunID: "run", TaskID: "task", HostID: "host", CommandID: "command", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1, ReservationID: "budget", GrantedActiveMS: 1000, AdapterPayload: raw})
			digest, err := m.Digest()
			if err != nil {
				t.Fatal(err)
			}
			scope, err := contract.GitFilterResourceScopeSHA256(g, digest)
			if err != nil {
				t.Fatal(err)
			}
			g.GitFilter = &contract.GitFilterResourceGrant{Version: 1, ReservationID: "filter", ScopeSHA256: scope, ManifestSHA256: digest, CleanToolSHA256: pin, SmudgeToolSHA256: pin, ToolMaximumBytes: contract.GitFilterToolMaximumBytes, StreamMaximumBytes: contract.GitFilterStreamMaximumBytes, StreamSlots: contract.GitFilterStreamSlots}
			g.LaunchIntentHash, err = contract.CandidateIntentHash(g)
			if err != nil {
				t.Fatal(err)
			}
			if err = contract.ValidateDependencyInputs(g); err != nil {
				t.Fatal("invalid test authority", err)
			}
			observed := false
			_, err = h.ExecuteLaunch(context.Background(), g, filterGateInvocation{observed: &observed})
			if err == nil || err.Error() != "git_filter_pipeline_unsupported" {
				t.Errorf("filter request reached unimplemented pipeline: %v", err)
			}
			if observed {
				t.Error("invocation inspected before filter refusal")
			}
			entries, readErr := os.ReadDir(h.spoolRoot)
			if readErr != nil || len(entries) != 0 || len(h.reservations) != 0 || len(h.active) != 0 {
				t.Fatalf("closed filter changed Host state: entries=%v reservations=%v active=%v error=%v", entries, h.reservations, h.active, readErr)
			}
		})
	}
}
