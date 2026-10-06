package host

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func codexTwoSealFixture(t *testing.T) (*Host, codexPreparedState, contract.LaunchCommand, adapter.Invocation, []string) {
	t.Helper()
	h, original, g, inv, env := codexContinuationFixture(t)
	p, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
	if err != nil {
		t.Fatal(err)
	}
	out, bound, err := codexRunContinuationPipe(t, h, p, g, "")
	if err != nil {
		t.Fatal(err)
	}
	g, inv = codexFinishSyntheticQuestion(t, h, p, g, bound, out.Completion.Text)
	return h, original, g, inv, env
}

func codexRewriteTestSeal(t *testing.T, root codexStateRoot, mutate func(*codexSealRecord)) {
	t.Helper()
	var record codexSealRecord
	if _, err := codexReadProtectedRecord(context.Background(), root, "codex-session-seal.json", &record); err != nil {
		t.Fatal(err)
	}
	mutate(&record)
	body, err := codexEncodeSeal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root.Path, "codex-session-seal.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCodexContinuationRejectsBrokenOrConflictingExactChain(t *testing.T) {
	for _, kind := range []string{"cycle", "broken", "attempt", "owner", "initial_previous", "ancestor_replacement", "duplicate_revision", "source_session", "source_question", "source_exit", "source_unknown", "initial_session"} {
		t.Run(kind, func(t *testing.T) {
			h, original, g, inv, env := codexTwoSealFixture(t)
			sourceRoot, err := codexAnchorStateRoot(context.Background(), filepath.Join(h.spoolRoot, g.AttemptID, g.Continuation.SourceSegmentID))
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "cycle":
				codexRewriteTestSeal(t, sourceRoot, func(r *codexSealRecord) { r.PreviousSegmentID = r.Source.SegmentID })
			case "broken":
				codexRewriteTestSeal(t, sourceRoot, func(r *codexSealRecord) { r.PreviousSegmentID = "missing" })
			case "attempt":
				g.AttemptID = "other-attempt"
			case "owner":
				g.Continuation.OwnerContextID = "other-owner"
			case "initial_previous":
				codexRewriteTestSeal(t, original.Protected, func(r *codexSealRecord) { r.PreviousSegmentID = "other"; r.PreviousSHA256 = hashText("other") })
			case "ancestor_replacement":
				path := filepath.Join(original.Protected.Path, "codex-session-seal.json")
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				replacement := filepath.Join(original.Protected.Path, "replacement")
				if os.WriteFile(replacement, body, 0600) != nil || os.Rename(replacement, path) != nil {
					t.Fatal("owned fixture replacement")
				}
			case "duplicate_revision":
				codexRewriteTestSeal(t, sourceRoot, func(r *codexSealRecord) { r.QuestionRevision = 1 })
			case "initial_session":
				codexRewriteTestSeal(t, sourceRoot, func(r *codexSealRecord) { r.InitialSession.EventID = "different" })
			case "source_session", "source_question", "source_exit", "source_unknown":
				spool, _ := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.Continuation.SourceSegmentID})
				events, err := spool.Read()
				if err != nil {
					t.Fatal(err)
				}
				for _, event := range events {
					selected := map[string]string{"source_session": contract.EventSession, "source_question": contract.EventQuestion, "source_exit": contract.EventExited, "source_unknown": contract.EventExited}[kind]
					if event.Kind != selected {
						continue
					}
					switch kind {
					case "source_session":
						event.SessionID = "22345678-1234-4321-8123-123456789abc"
					case "source_question":
						event.CommandID = "foreign-command"
					case "source_exit":
						if err := os.Remove(filepath.Join(sourceRoot.Path, fmt.Sprintf("event-%020d.json", event.Sequence))); err != nil {
							t.Fatal(err)
						}
						continue
					case "source_unknown":
						event.Kind = contract.EventUnknown
						event.ExitCode = nil
					}
					body, err := json.Marshal(event)
					if err != nil || os.WriteFile(filepath.Join(sourceRoot.Path, fmt.Sprintf("event-%020d.json", event.Sequence)), body, 0600) != nil {
						t.Fatal("owned event fixture mutation")
					}
				}
			}
			if _, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{}); err == nil {
				t.Fatal("broken/contradictory chain admitted")
			}
			if len(h.active) != 0 || len(h.codexLeases) != 0 {
				t.Fatal("restore created process authority")
			}
		})
	}
}

func TestCodexContinuationFinalPrestartBarrierRejectsEarlierObservedDrift(t *testing.T) {
	for _, kind := range []string{"main", "wal", "aux", "seal", "thread", "source", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			h, original, g, inv, env := codexContinuationFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reached := false
			_, err := h.prepareManagedCodexState(ctx, g, inv, env, codexPreparationControl{BeforePublish: func() {
				reached = true
				var path string
				switch kind {
				case "main":
					path = filepath.Join(original.Layout.SQLite.Path, "state_5.sqlite")
				case "wal":
					path = filepath.Join(original.Layout.SQLite.Path, "state_5.sqlite-wal")
				case "aux":
					path = filepath.Join(original.Layout.Auxiliary.Runtime[0].Path, "skill")
				case "seal":
					path = filepath.Join(original.Protected.Path, "codex-session-seal.json")
				case "thread":
					path = filepath.Join(original.Protected.Path, "codex-thread.json")
				case "source":
					path = filepath.Join(original.Source.Home, "config.toml")
				case "cancel":
					cancel()
					return
				}
				body := []byte("changed owned synthetic fixture")
				if kind == "seal" || kind == "thread" || kind == "source" {
					var err error
					body, err = os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
				}
				replacement := filepath.Join(filepath.Dir(path), "replacement")
				if os.WriteFile(replacement, body, 0600) != nil || os.Rename(replacement, path) != nil {
					t.Fatal("fixture mutation")
				}
			}})
			if !reached || err == nil {
				t.Fatal("final pre-Start barrier missing")
			}
		})
	}
}
