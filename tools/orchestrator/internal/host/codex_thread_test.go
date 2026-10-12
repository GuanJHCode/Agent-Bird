package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// These are synthetic provenance fixtures. No process is launched and the
// phase rows do not claim to establish an OS exit/Wait capability.
func codexThreadFixture(t *testing.T) (*Host, codexPreparedState, contract.LaunchCommand, process.Identity, codexsession.ThreadBinding) {
	t.Helper()
	h, g, inv, env := codexOriginFixture(t)
	p, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
	if err != nil {
		t.Fatal(err)
	}
	_, p, err = codexManagedCommand(context.Background(), p, env, h.spoolRoot)
	if err != nil {
		t.Fatal(err)
	}
	id := process.Identity{PID: 1234, PGID: 1234, Birth: "synthetic-original", BirthKnown: true, Executable: p.Launch.Path, ExecutableSHA256: p.Launch.SHA256}
	b := codexsession.ThreadBinding{ThreadID: "12345678-1234-4321-8123-123456789abc", Model: "gpt-fixed", ModelProvider: "openai", HistoryMode: "paginated"}
	b.RolloutPath = filepath.Join(p.Layout.Home.Path, "sessions/2026/10/05/rollout-2026-10-05T01-02-03-"+b.ThreadID+".jsonl")
	spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	if err != nil {
		t.Fatal(err)
	}
	meta := launchMetadata{workRevision: g.WorkRevision, executionEpoch: g.ExecutionEpoch, commandID: g.CommandID}
	for _, kind := range []string{contract.EventSpawned, contract.EventRunning} {
		if _, err := h.appendPhaseEvent(spool, g.RunID, g.TaskID, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, meta, kind, hashText(kind), &id, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	return h, p, g, id, b
}

func TestCodexThreadBindingWritesProtectedRecordBeforeDurableSession(t *testing.T) {
	h, p, g, id, b := codexThreadFixture(t)
	bound, err := h.bindManagedCodexThread(context.Background(), p, g, id, b)
	if err != nil {
		t.Fatal(err)
	}
	var read codexThreadRecord
	file, err := codexReadProtectedRecord(context.Background(), p.Protected, "codex-thread.json", &read)
	if err != nil || file != bound.File || read.Origin != p.OriginFile || read.Binding.ThreadID != b.ThreadID || read.First.CommandID != g.CommandID || read.First.Process.PID != id.PID {
		t.Fatal("original thread binding lost", err)
	}
	spool, _ := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	records, err := spool.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || records[2].Kind != contract.EventSession || records[2].EventID != bound.Session.EventID || records[2].SessionID != b.ThreadID || records[2].Sequence <= records[1].Sequence {
		t.Fatal("Session not durably appended after binding")
	}
	if _, err := os.Lstat(b.RolloutPath); !os.IsNotExist(err) {
		t.Fatal("callback fabricated or required deferred rollout")
	}
	if _, err := h.bindManagedCodexThread(context.Background(), p, g, id, b); err == nil {
		t.Fatal("duplicate callback authorized another turn")
	}
	after, _ := spool.Read()
	if len(after) != 3 {
		t.Fatal("duplicate Session appended")
	}
}

func TestCodexThreadBindingRejectsInvalidScopeBeforeSession(t *testing.T) {
	for _, kind := range []string{"empty_path", "legacy_history", "foreign_uuid", "model_drift", "source_drift", "wrong_command", "wrong_process", "not_running", "unknown", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			h, p, g, id, b := codexThreadFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			spool, _ := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
			switch kind {
			case "empty_path":
				b.RolloutPath = ""
			case "legacy_history":
				b.HistoryMode = "legacy"
			case "foreign_uuid":
				b.ThreadID = "ffffffff-1234-4321-8123-123456789abc"
			case "model_drift":
				b.Model = "other"
			case "source_drift":
				if err := os.WriteFile(filepath.Join(p.Source.Home, "config.toml"), []byte("model=\"changed\"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong_command":
				g.CommandID = "other"
			case "wrong_process":
				id.PID++
			case "not_running":
				if err := os.Remove(filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, "event-00000000000000000002.json")); err != nil {
					t.Fatal(err)
				}
			case "unknown":
				if _, err := h.appendPhaseEvent(spool, g.RunID, g.TaskID, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, launchMetadata{workRevision: g.WorkRevision, executionEpoch: g.ExecutionEpoch, commandID: g.CommandID}, contract.EventUnknown, hashText("unknown"), &id, nil, nil); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				cancel()
			}
			if _, err := h.bindManagedCodexThread(ctx, p, g, id, b); err == nil {
				t.Fatal("untrusted binding authorized Session")
			}
			records, err := spool.Read()
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range records {
				if event.Kind == contract.EventSession {
					t.Fatal("Session emitted on rejected binding")
				}
			}
			if _, err := os.Lstat(filepath.Join(p.Protected.Path, "codex-thread.json")); !os.IsNotExist(err) {
				t.Fatal("invalid callback published a binding")
			}
		})
	}
}
