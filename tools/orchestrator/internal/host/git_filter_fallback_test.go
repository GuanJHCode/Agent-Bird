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
