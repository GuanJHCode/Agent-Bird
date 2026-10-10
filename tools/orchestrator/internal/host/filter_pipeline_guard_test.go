package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitfilter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func filterGuardGrant(t *testing.T) (contract.LaunchCommand, adapter.InvocationPayload) {
	t.Helper()
	sha := strings.Repeat("a", 64)
	m := &contract.GitFilterManifestV1{Version: 1, DriverID: "fixture", Paths: []string{"file"}, Git: contract.GitFilterGitPin{Path: "/private/git", Version: "fixture", SHA256: sha}, Clean: contract.GitFilterToolPin{Path: "/private/filter", SHA256: sha, Args: []string{}}, Smudge: contract.GitFilterToolPin{Path: "/private/filter", SHA256: sha, Args: []string{}}, Attributes: []contract.GitFilterAttributePin{}, ConfigSHA256: sha}
	p := adapter.InvocationPayload{Kind: "provider", Provider: string(adapter.ProviderClaude), Directory: "/private/candidate", GitFilter: m, ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: adapter.BinaryPin{Path: "/private/provider", Version: "fixture", SHA256: sha}}, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: 1000}, CandidateWorkspace: &adapter.CandidateWorkspace{Version: 1, AutoDirectory: true, RepoRoot: "/private/repo", BaseOID: strings.Repeat("b", 40), Paths: []string{"file"}}}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	g := withHostArtifactsForTest(t, contract.LaunchCommand{RunID: "run", TaskID: "task", HostID: "host", CommandID: "command", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1, ReservationID: "budget", GrantedActiveMS: 1000, AdapterPayload: raw})
	sha, err = m.Digest()
	if err != nil {
		t.Fatal(err)
	}
	scope, err := contract.GitFilterResourceScopeSHA256(g, sha)
	if err != nil {
		t.Fatal(err)
	}
	g.GitFilter = &contract.GitFilterResourceGrant{Version: 1, ReservationID: "filter", ScopeSHA256: scope, ManifestSHA256: sha, CleanToolSHA256: m.Clean.SHA256, SmudgeToolSHA256: m.Smudge.SHA256, ToolMaximumBytes: contract.GitFilterToolMaximumBytes, StreamMaximumBytes: contract.GitFilterStreamMaximumBytes, StreamSlots: contract.GitFilterStreamSlots}
	g.LaunchIntentHash, err = contract.CandidateIntentHash(g)
	if err != nil {
		t.Fatal(err)
	}
	return g, p
}

func TestFilterRuntimeFreezesAcceptedInputBytes(t *testing.T) {
	g, _ := filterGuardGrant(t)
	exit := 0
	input := contract.AcceptedInput{AdapterPayload: json.RawMessage("{ \"kind\": \"provider\" }"), DecisionCommandID: "accepted", Event: contract.Event{Artifact: &contract.ArtifactRef{Path: "/private/artifact", SHA256: "source"}, Process: &contract.ProcessIdentity{PID: 123}, ExitCode: &exit}}
	g.Input = &input
	g.DependencyInputs = &contract.DependencyInputVector{Version: 1, Inputs: []contract.AcceptedInput{input}}
	original, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	f, err := newFilterRuntime(g)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.grant.AdapterPayload, g.AdapterPayload) || !bytes.Equal(f.grant.Input.AdapterPayload, input.AdapterPayload) || !bytes.Equal(f.grant.DependencyInputs.Inputs[0].AdapterPayload, input.AdapterPayload) {
		t.Fatal("frozen grant compacted input bytes")
	}
	g.AdapterPayload[0] = '['
	g.Input.AdapterPayload[0] = '['
	g.Input.Event.Artifact.Path = "/changed"
	g.Input.Event.Process.PID = 999
	*g.Input.Event.ExitCode = 9
	g.DependencyInputs.Inputs[0].DecisionCommandID = "changed"
	g.GitFilter.ReservationID = "changed"
	frozen, err := json.Marshal(f.grant)
	if err != nil || !bytes.Equal(frozen, original) {
		t.Fatalf("caller changed frozen authority: %v", err)
	}
}

func TestFilterEntryRejectsBeforeInvocation(t *testing.T) {
	for _, mode := range []string{"missing_grant", "scope_drift", "continuation", "session"} {
		t.Run(mode, func(t *testing.T) {
			g, _ := filterGuardGrant(t)
			switch mode {
			case "missing_grant":
				g.GitFilter = nil
			case "scope_drift":
				g.GitFilter.ScopeSHA256 = strings.Repeat("f", 64)
			case "continuation":
				g.Continuation = &contract.ContinuationRef{Version: 1}
			case "session":
				g.SessionID = "prior"
			}
			h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "host")
			if err != nil {
				t.Fatal(err)
			}
			observed := false
			if _, err = h.ExecuteLaunch(context.Background(), g, filterGateInvocation{observed: &observed}); err == nil || observed {
				t.Fatalf("unbound filtered invocation observed=%t err=%v", observed, err)
			}
			entries, err := os.ReadDir(h.spoolRoot)
			if err != nil || len(entries) != 0 || len(h.reservations) != 0 {
				t.Fatalf("refusal changed Host state: %v %v", entries, err)
			}
		})
	}
}

// The journal is deliberately synthetic and contains no process intent. No
// executable exists at these pins; O_EXCL must fail before any pin or Start.
func TestFilterColdJournalCannotSettle(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("filtered Host admission is Darwin-only")
	}
	g, p := filterGuardGrant(t)
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "host")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(h.spoolRoot, ".gitops-journal", filterJournalKey(g))
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "events.jsonl")
	body := []byte("{\"partial\":")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	inv := filterInvocation{argv: []string{p.ProviderLock.Binary.Path}, payload: p, pinPath: p.ProviderLock.Binary.Path, pinSHA: p.ProviderLock.Binary.SHA256}
	r, err := h.ExecuteLaunch(context.Background(), g, inv)
	if err == nil || r.Status != "unknown" {
		t.Fatalf("cold journal settled: result=%+v err=%v", r, err)
	}
	events, err := h.Collect(context.Background(), store.Attempt{ID: g.AttemptID, TaskID: g.TaskID, SegmentID: g.SegmentID})
	if err != nil {
		t.Fatal(err)
	}
	unknown := false
	for _, e := range events {
		if e.Kind == contract.EventUnknown {
			unknown = true
		}
		if e.Kind == contract.EventExited || e.Kind == contract.EventFailed || e.Kind == contract.EventLaunchRejected || e.GitFilterCleanup != nil || e.ValidationToolCleanup != nil {
			t.Fatalf("cold state fabricated terminal proof: %+v", e)
		}
	}
	after, err := os.ReadFile(path)
	if !unknown || err != nil || !bytes.Equal(after, body) {
		t.Fatalf("cold journal changed/unknown missing: %t %v", unknown, err)
	}
}

type filterUnopenedBroker struct{ gitfilter.ResourceBroker }
type filterUnopenedJournal struct{ filterHostJournal }

func TestFilterFinalizerCannotUseColdProcess(t *testing.T) {
	var cold process.Handle
	if err := json.Unmarshal([]byte(`{"originalSpawnedAt":"2026-10-10T00:00:00Z"}`), &cold); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	f := &filterRuntime{opened: true, providerAttempted: true, broker: filterUnopenedBroker{}, journal: filterUnopenedJournal{}, source: &gitops.FilterGitContext{}, provider: &cold, providerStartedAt: now, providerTerminalAt: now.Add(time.Millisecond)}
	if err := f.requireOriginalTerminal(); !errors.Is(err, process.ErrProcessTreeUnknown) {
		t.Fatalf("cold serialized process accepted: %v", err)
	}
	if !reflect.DeepEqual(cold.OriginalSpawnedAt(), time.Time{}) {
		t.Fatal("cold process acquired a Start timestamp")
	}
}
