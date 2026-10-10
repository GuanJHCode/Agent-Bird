package host

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestGitFilterFallbackCannotInventNeverStarted(t *testing.T) {
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "host")
	if err != nil {
		t.Fatal(err)
	}
	g := withHostArtifactsForTest(t, contract.LaunchCommand{RunID: "run", TaskID: "task", HostID: "host", CommandID: "command", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1})
	g.GitFilter = &contract.GitFilterResourceGrant{Version: 1}
	meta := metadataForLaunch(g)
	g.GitFilter.Version = 99
	g.GitFilter = nil
	if meta.artifactGrant.GitFilter.Version != 1 {
		t.Error("metadata retained mutable filter authority")
	}
	a := store.Attempt{ID: g.AttemptID, TaskID: g.TaskID, SegmentID: g.SegmentID}
	if err = h.recordLaunchFailed(a, g.RunID, g.TaskID, meta, errors.New("outer_fallback")); err == nil {
		t.Error("fallback fabricated a never-started filter outcome")
	}
	events, err := h.Collect(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("fallback wrote terminal events without original runtime: %+v", events)
	}
}

func TestGitFilterSourceFallbackRetainsUnknown(t *testing.T) {
	for _, mode := range []string{"before_dispatch", "invocation_failure", "execute_refusal"} {
		t.Run(mode, func(t *testing.T) {
			h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "host")
			if err != nil {
				t.Fatal(err)
			}
			g := withHostArtifactsForTest(t, contract.LaunchCommand{RunID: "run", TaskID: "task", HostID: "host", CommandID: "command", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1})
			// A declared but unusable filter cannot acquire a live cleanup proof
			// from any outer preparation/refusal path.
			g.GitFilter = &contract.GitFilterResourceGrant{Version: 1}
			r := &livingSource{host: h, ctx: context.Background(), working: map[string]bool{}, failure: func(err error) { t.Errorf("source failed to retain UNKNOWN: %v", err) }}
			if mode == "before_dispatch" {
				if err = r.recordUnstartedGrant(g, errors.New("refused")); err != nil {
					t.Error(err)
				}
			} else {
				r.cfg.InvocationProvider = func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
					if mode == "invocation_failure" {
						return nil, errors.New("invocation_refused")
					}
					return launchInvocation{args: []string{"/never-executed"}}, nil
				}
				r.workers.Add(1)
				r.execute(g, false)
			}
			events, err := h.Collect(context.Background(), store.Attempt{ID: g.AttemptID, TaskID: g.TaskID, SegmentID: g.SegmentID})
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || events[0].Kind != contract.EventUnknown || events[0].GitFilterCleanup != nil || events[0].ActiveMS != 0 {
				t.Fatalf("outer source lost uncertainty: %+v", events)
			}
		})
	}
}
