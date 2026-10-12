package host

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func codexSealFixture(t *testing.T) (*Host, codexPreparedState, contract.LaunchCommand, codexBoundThread) {
	t.Helper()
	h, p, g, id, b := codexThreadFixture(t)
	bound, err := h.bindManagedCodexThread(context.Background(), p, g, id, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range codexSQLiteFamilies {
		body := make([]byte, 100)
		copy(body, "SQLite format 3\x00")
		if err := os.WriteFile(filepath.Join(p.Layout.SQLite.Path, family+".sqlite"), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(b.RolloutPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.RolloutPath, []byte("{\"synthetic\":true}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Layout.Auxiliary.Runtime[0].Path, "skill"), []byte("auxiliary bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	return h, p, g, bound
}

func TestCodexQuestionSealCandidateRetainsExactSessionAndOneBudget(t *testing.T) {
	h, p, g, bound := codexSealFixture(t)
	remaining := codexStateTotalLimit
	candidate, err := h.captureManagedCodexQuestionSeal(context.Background(), p, g, bound, 1, hashText("question"), codexStateReadControl{Remaining: &remaining}, codexSealControl{})
	if err != nil {
		t.Fatal(err)
	}
	r := candidate.Record
	if r.Version != 1 || r.Origin != p.OriginFile || r.Thread != bound.File || r.InitialSession != codexSessionEvidence(bound.Session) || r.CurrentSession != r.InitialSession || r.Source != bound.Record.First || r.PreviousSHA256 != "" || r.QuestionRevision != 1 || len(r.State.Slots) != 20 || r.State.Bytes != codexStateTotalLimit-remaining || !reflect.DeepEqual(r.State.RequiredAbsences, codexRequiredAbsences()) {
		t.Fatal("seal dropped origin/session/state proof")
	}
	body, err := codexEncodeSeal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded codexSealRecord
	if codexDecodeProtectedRecord(body, 16<<10, &decoded) != nil || !reflect.DeepEqual(decoded, r) {
		t.Fatal("seal shape did not round-trip losslessly")
	}
	if _, err := os.Lstat(filepath.Join(p.Protected.Path, "codex-session-seal.json")); !os.IsNotExist(err) {
		t.Fatal("candidate falsely published without original exit")
	}
	if _, err := h.publishManagedCodexQuestionSeal(context.Background(), candidate, codexOriginalExit{}, codexsession.Outcome{}); err == nil {
		t.Fatal("seal accepted nonexistent original Wait/tree capability")
	}
}

func TestCodexQuestionSealCandidateUsesOneFixedDefaultBudget(t *testing.T) {
	h, p, g, bound := codexSealFixture(t)
	// Exactly the control supplied by the actual ExecuteLaunch consumer.
	candidate, err := h.captureManagedCodexQuestionSeal(context.Background(), p, g, bound, 1, hashText("question"), codexStateReadControl{}, codexSealControl{})
	if err != nil || len(candidate.Record.State.Slots) != 20 || candidate.Record.State.Bytes <= 0 || candidate.Record.State.Bytes > codexStateTotalLimit {
		t.Fatal("actual launch consumer lacked its single fixed capture budget", err)
	}
}

func TestCodexQuestionSealCandidateNeverRefillsExplicitSharedBudget(t *testing.T) {
	h, p, g, bound := codexSealFixture(t)
	full, err := h.captureManagedCodexQuestionSeal(context.Background(), p, g, bound, 1, hashText("question"), codexStateReadControl{}, codexSealControl{})
	if err != nil || full.Record.State.Bytes < 2 {
		t.Fatal("baseline opaque capture unavailable", err)
	}
	for _, budget := range []int64{0, 1, 500, full.Record.State.Bytes - 1} {
		remaining := budget
		if _, err := h.captureManagedCodexQuestionSeal(context.Background(), p, g, bound, 1, hashText("question"), codexStateReadControl{Remaining: &remaining}, codexSealControl{}); err == nil || remaining < 0 || remaining > budget {
			t.Fatal("explicit exhausted/small budget was replaced or exceeded", budget, remaining, err)
		}
	}
	remaining := full.Record.State.Bytes
	if exact, err := h.captureManagedCodexQuestionSeal(context.Background(), p, g, bound, 1, hashText("question"), codexStateReadControl{Remaining: &remaining}, codexSealControl{}); err != nil || remaining != 0 || exact.Record.State.Bytes != full.Record.State.Bytes {
		t.Fatal("fixed and auxiliary reads did not consume one exact budget", err, remaining)
	}
	if _, err := h.captureManagedCodexQuestionSeal(context.Background(), p, g, bound, 1, hashText("question"), codexStateReadControl{Remaining: &remaining}, codexSealControl{}); err == nil || remaining != 0 {
		t.Fatal("retained depleted budget acquired a fresh window")
	}
}

func TestCodexQuestionSealCandidateFinalBarrierRejectsEarlierStateOrReferences(t *testing.T) {
	for _, kind := range []string{"earlier_main", "absent_wal", "thread_same_bytes", "origin_same_bytes", "source_same_bytes", "source_auth_same_bytes", "owned_auth_link", "cache", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			h, p, g, bound := codexSealFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			remaining := codexStateTotalLimit
			reached := false
			_, err := h.captureManagedCodexQuestionSeal(ctx, p, g, bound, 1, hashText("question"), codexStateReadControl{Remaining: &remaining}, codexSealControl{BeforeFinalVerify: func() {
				reached = true
				var path string
				body := []byte("changed")
				switch kind {
				case "earlier_main":
					path = filepath.Join(p.Layout.SQLite.Path, "state_5.sqlite")
					body = make([]byte, 100)
					copy(body, "SQLite format 3\x00")
					body[99] = 1
				case "absent_wal":
					path = filepath.Join(p.Layout.SQLite.Path, "state_5.sqlite-wal")
					body = nil
				case "cache":
					path = filepath.Join(p.Layout.Home.Path, "models_cache.json")
				case "thread_same_bytes":
					path = filepath.Join(p.Protected.Path, "codex-thread.json")
				case "origin_same_bytes":
					path = filepath.Join(p.Protected.Path, "codex-origin.json")
				case "source_same_bytes":
					path = filepath.Join(p.Source.Home, "config.toml")
				case "source_auth_same_bytes":
					path = filepath.Join(p.Source.Home, "auth.json")
					body = []byte("SYNTHETIC_AUTH_NEVER_READ")
				case "owned_auth_link":
					path = filepath.Join(p.Layout.Home.Path, "auth.json")
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(p.Origin.Owned.AuthTarget, path); err != nil {
						t.Fatal(err)
					}
					return
				case "cancel":
					cancel()
					return
				}
				if kind == "thread_same_bytes" || kind == "origin_same_bytes" || kind == "source_same_bytes" {
					var err error
					body, err = os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
				} // Owned controls only; never source auth bytes.
				tmp := filepath.Join(filepath.Dir(path), "replacement")
				if err := os.WriteFile(tmp, body, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(tmp, path); err != nil {
					t.Fatal(err)
				}
			}})
			if !reached || err == nil || remaining >= codexStateTotalLimit {
				t.Fatal("final composition barrier not exercised/rejected", reached, err)
			}
			if _, err := os.Lstat(filepath.Join(p.Protected.Path, "codex-session-seal.json")); !os.IsNotExist(err) {
				t.Fatal("failed capture published a seal")
			}
		})
	}
}

func TestCodexQuestionSealCandidateRejectsNonresumableOrConflictingProvenance(t *testing.T) {
	for _, kind := range []string{"stopped", "unknown", "exited_row_without_capability", "wrong_event", "wrong_epoch", "wrong_process", "revision", "payload", "missing_main", "installation"} {
		t.Run(kind, func(t *testing.T) {
			h, p, g, bound := codexSealFixture(t)
			switch kind {
			case "stopped", "unknown", "exited_row_without_capability":
				spool, _ := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
				eventKind := map[string]string{"stopped": contract.EventStopped, "unknown": contract.EventUnknown, "exited_row_without_capability": contract.EventExited}[kind]
				if _, err := h.appendPhaseEvent(spool, g.RunID, g.TaskID, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, launchMetadata{workRevision: g.WorkRevision, executionEpoch: g.ExecutionEpoch, commandID: g.CommandID}, eventKind, hashText(eventKind), nil, nil, nil); err != nil {
					t.Fatal(err)
				}
			case "wrong_event":
				bound.Session.EventID = "other"
			case "wrong_epoch":
				g.ExecutionEpoch++
			case "wrong_process":
				bound.Record.First.Process.PID++
			case "missing_main":
				if err := os.Remove(filepath.Join(p.Layout.SQLite.Path, "goals_1.sqlite")); err != nil {
					t.Fatal(err)
				}
			case "installation":
				if err := os.WriteFile(filepath.Join(p.Layout.Home.Path, "installation_id"), []byte("11111111-2222-3333-4444-555555555555"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			revision, payload := 1, hashText("question")
			if kind == "revision" {
				revision = 2
			}
			if kind == "payload" {
				payload = ""
			}
			remaining := codexStateTotalLimit
			if _, err := h.captureManagedCodexQuestionSeal(context.Background(), p, g, bound, revision, payload, codexStateReadControl{Remaining: &remaining}, codexSealControl{}); err == nil {
				t.Fatal("nonresumable provenance accepted")
			}
		})
	}
}
