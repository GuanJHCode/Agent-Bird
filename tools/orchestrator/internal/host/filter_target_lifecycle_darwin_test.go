//go:build darwin

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// No Git/filter/Provider process is launched in this oracle. It checks the
// original Host callback lifecycle and real terminal artifact publication.
func TestFilterTargetLateStopRetainsTerminalArtifact(t *testing.T) {
	for _, mode := range []string{"stop", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			root, _ := filterPipelineRoot(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			h, err := NewIPC(filepath.Join(root, "spool"), "host")
			if err != nil {
				t.Fatal(err)
			}
			p := filterReviewGuardPayload(t)
			p.Kind, p.Provider, p.Profile, p.ProviderLock = "candidate", "", nil, nil
			p.Directory = root
			p.CandidateAction = &adapter.CandidateAction{Version: 1, Operation: "filter_target_preflight", SourceTask: "review", DependencyInputVersion: 1, AutoDirectory: true, Target: adapter.CandidateTarget{Worktree: root, Ref: "refs/heads/main", BaseOID: strings.Repeat("a", 40)}}
			// Only sealing is exercised; this real executable is never started.
			pin, err := process.ExecutableDigest(ctx, "/bin/cat")
			if err != nil {
				t.Fatal(err)
			}
			p.GitFilter.Git = contract.GitFilterGitPin{Path: "/bin/cat", Version: "unstarted-fixture", SHA256: pin}
			p.GitFilter.Clean = contract.GitFilterToolPin{Path: "/bin/cat", SHA256: pin, Args: []string{}}
			p.GitFilter.Smudge = p.GitFilter.Clean
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			g := withHostArtifactsForTest(t, contract.LaunchCommand{RunID: "run", TaskID: "observe", HostID: "host", CommandID: "command", AttemptID: "attempt", SegmentID: "segment", PlanRevision: 1, WorkRevision: 1, ExecutionEpoch: 1, AdapterPayload: raw})
			sha, err := p.GitFilter.Digest()
			if err != nil {
				t.Fatal(err)
			}
			scope, err := contract.GitFilterResourceScopeSHA256(g, sha)
			if err != nil {
				t.Fatal(err)
			}
			g.GitFilter = &contract.GitFilterResourceGrant{Version: 1, ReservationID: "filter", ScopeSHA256: scope, ManifestSHA256: sha, CleanToolSHA256: pin, SmudgeToolSHA256: pin, ToolMaximumBytes: contract.GitFilterToolMaximumBytes, StreamMaximumBytes: contract.GitFilterStreamMaximumBytes, StreamSlots: 3}
			f, err := newFilterRuntime(g)
			if err != nil {
				t.Fatal(err)
			}
			meta := metadataForLaunch(g)
			meta.filter = f
			body := []byte("unpublished observation")
			meta.runFilterObservation = func(context.Context) ([]byte, error) { return body, nil }
			starts := 0
			meta.startProcess = func(context.Context, process.Command) (*process.Handle, error) {
				starts++
				return nil, errors.New("unexpected provider start")
			}
			var materialized *contract.ArtifactRef
			meta.afterObservationArtifact = func(ref *contract.ArtifactRef) {
				materialized = ref
				if got, e := os.ReadFile(ref.Path); e != nil || string(got) != string(body) {
					t.Fatalf("real terminal slot missing: %v", e)
				}
				if mode == "stop" {
					if e := h.StopSegment(ctx, g.SegmentID); e != nil {
						t.Fatal(e)
					}
				} else {
					cancel()
				}
			}
			a := store.Attempt{ID: g.AttemptID, TaskID: g.TaskID, SegmentID: g.SegmentID}
			result, runErr := h.execute(ctx, a, g.RunID, g.TaskID, process.Command{}, false, meta)
			events, err := h.Collect(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			var kinds []string
			for _, e := range events {
				kinds = append(kinds, e.Kind)
				if e.Process != nil || e.ExitCode != nil || e.SessionID != "" || e.SessionKind != "" {
					t.Fatal("invented provider identity")
				}
				if e.Kind == contract.EventStopped || e.Kind == contract.EventExited {
					if materialized == nil || !reflect.DeepEqual(e.Artifact, materialized) {
						t.Fatal("stopped terminal artifact became unreferenced")
					}
				}
				if e.Kind == contract.EventStopped && e.StopReason == "" {
					t.Fatal("stop reason missing")
				}
				if e.Kind == contract.EventExited && (e.ActiveMS < 1 || e.GitFilterCleanup == nil || e.GitFilterCleanup.Validate() != nil) {
					t.Fatal("callback time/cleanup missing")
				}
			}
			if !errors.Is(runErr, context.Canceled) || result.Status != "interrupted" || result.ExitCode != -1 || starts != 0 || !reflect.DeepEqual(kinds, []string{contract.EventPrepared, contract.EventStopped, contract.EventExited}) {
				t.Fatalf("late cancellation result=%+v err=%v starts=%d events=%v", result, runErr, starts, kinds)
			}
			var d artifacts.HostSlotDescriptor
			var proof artifacts.Proof
			if materialized == nil || json.Unmarshal(materialized.HostArtifact.Descriptor, &d) != nil || json.Unmarshal(materialized.HostArtifact.Proof, &proof) != nil {
				t.Fatal("artifact proof missing")
			}
			got, err := artifacts.ReadVerifiedHostSlot(context.Background(), h.spoolRoot, d, proof)
			if err != nil || string(got) != string(body) {
				t.Fatalf("stopped slot lost: %v", err)
			}
			if _, err = os.Lstat(filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, ".git-filter")); !os.IsNotExist(err) {
				t.Fatal("known cleanup not completed", err)
			}
		})
	}
}
