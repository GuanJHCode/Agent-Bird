package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestAGYHistoryLifecycleSealsBeforeQuestion(t *testing.T) {
	for _, state := range []string{"present", "missing", "symlink", "partial_stage", "sqlite_clean", "sqlite_sidecar"} {
		t.Run(state, func(t *testing.T) {
			h, cmd, profile, g, _ := agyHistoryFixture(t)
			g.CommandID = "history-command-" + g.SegmentID
			g = managedHostGrant(t, h, g)
			if err := h.recordContinuationOrigin(g, nil); err != nil {
				t.Fatal(err)
			}
			first, err := h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "scratch"), g)
			if err != nil {
				t.Fatal(err)
			}
			owned := filepath.Join(commandEnvironment(cmd, "HOME"), ".gemini", agyHistoryArgument(t, first))
			conversations := filepath.Join(owned, "conversations")
			if err = os.Mkdir(conversations, 0700); err != nil {
				t.Fatal(err)
			}
			session := "9adeef5e-cd28-4c60-9858-a3462e408814"
			trajectory := filepath.Join(conversations, session+".pb")
			if state == "sqlite_clean" || state == "sqlite_sidecar" {
				trajectory = filepath.Join(conversations, session+".db")
			}
			writePath := ""
			if state == "present" || state == "partial_stage" || state == "sqlite_clean" || state == "sqlite_sidecar" {
				writePath = trajectory
			} else if state == "symlink" {
				target := filepath.Join(cmd.Dir, "foreign-state")
				if err = os.WriteFile(target, []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(target, trajectory); err != nil {
					t.Fatal(err)
				}
			}
			if state == "partial_stage" {
				stage := filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, "."+agySealName+".tmp")
				if err = os.WriteFile(stage, []byte(`{"version":`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			init := `{"event":"init","conversation_id":"` + session + `"}`
			terminal := `{"event":"result","result":{"conversation_id":"` + session + `","status":"SUCCESS","response":"fixture commentary","structured_output":{"version":1,"kind":"technical_question","text":"fixture question"}}}`
			local := process.Command{Path: "/bin/sh", Dir: cmd.Dir, Args: []string{"-c", `if [ -n "$1" ]; then printf fixture-state > "$1"; fi; printf '%s\n' "$2" "$3"`, "owned-AGY-frame-fixture", writePath, init, terminal}}
			if state == "sqlite_clean" || state == "sqlite_sidecar" {
				// Opaque synthetic SQLite-header bytes, not a real SQL writer.
				local.Args[1] = `umask 077; printf 'SQLite format 3\000%0112d' 0 > "$1"; if [ "$4" = sqlite_sidecar ]; then : > "$1-wal"; fi; printf '%s\n' "$2" "$3"`
				local.Args = append(local.Args, state)
			}
			var original *process.Handle
			meta := launchMetadata{commandID: g.CommandID, artifactGrant: metadataForLaunch(g).artifactGrant, completionMode: adapter.TaskCompletionV1, requireSession: true, outputProvider: "antigravity-cli",
				workRevision: 1, agyHistoryGrant: &g, afterWait: func(p *process.Handle, _ context.Context, _ error) { original = p }}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			result, err := h.execute(ctx, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, g.RunID, g.TaskID, local, false, meta)
			if original == nil {
				t.Fatalf("original Handle not observed: result=%s err=%v", result.Status, err)
			}
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), time.Second)
				defer stop()
				if err := original.Stop(cleanup); err != nil {
					t.Errorf("owned fixture original Handle cleanup unproved: %v", err)
				}
			})
			if err = original.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			if err = original.ConfirmTreeExited(); err != nil {
				t.Fatal(err)
			}
			t.Logf("owned_fixture_cleanup original_wait=true group_absent=true pid=%d pgid=%d birth=%s", original.Identity().PID, original.Identity().PGID, original.Identity().Birth)
			spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
			if err != nil {
				t.Fatal(err)
			}
			events, err := spool.Read()
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, e := range events {
				counts[e.Kind]++
			}
			if counts[contract.EventExited] != 1 || counts[contract.EventUnknown] != 0 {
				t.Fatalf("proved actual exit was lost: %v", counts)
			}
			var seal agyHistorySeal
			sealErr := readContinuationMetadata(filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, agySealName), &seal)
			if state == "present" || state == "sqlite_clean" {
				if result.Status != "waiting_user" || counts[contract.EventQuestion] != 1 || counts[contract.EventFailed] != 0 || sealErr != nil || seal.Process.PID != original.Identity().PID || seal.SessionID != session {
					t.Fatalf("Question published without its original-Handle state seal: result=%s counts=%v sealErr=%v", result.Status, counts, sealErr)
				}
				if state == "sqlite_clean" && (seal.Version != 2 || seal.StorageKind != "sqlite-v1") {
					t.Fatal("actual completed SQLite actor was not bound to its explicit format")
				}
			} else if result.Status != "failed" || counts[contract.EventQuestion] != 0 || counts[contract.EventFailed] != 1 || sealErr == nil {
				t.Fatalf("unsealed state published answerable Question: result=%s counts=%v sealErr=%v", result.Status, counts, sealErr)
			}
		})
	}
}

func TestAGYHistoryLifecycleStopPreservesFactsWhenSealUnavailable(t *testing.T) {
	for _, state := range []string{"present", "missing", "no_session", "sqlite_clean", "sqlite_sidecar"} {
		t.Run(state, func(t *testing.T) {
			h, cmd, profile, g, originalApp := agyHistoryFixture(t)
			g.CommandID = "history-command-" + g.SegmentID
			g = managedHostGrant(t, h, g)
			if err := h.recordContinuationOrigin(g, nil); err != nil {
				t.Fatal(err)
			}
			prepared, err := h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "scratch"), g)
			if err != nil {
				t.Fatal(err)
			}
			owned := filepath.Join(filepath.Dir(originalApp), agyHistoryArgument(t, prepared))
			if err = os.Mkdir(filepath.Join(owned, "conversations"), 0700); err != nil {
				t.Fatal(err)
			}
			session := "9adeef5e-cd28-4c60-9858-a3462e408814"
			init := `{"event":"init","conversation_id":"` + session + `"}`
			writePath := ""
			if state == "present" {
				writePath = filepath.Join(owned, "conversations", session+".pb")
			} else if state == "sqlite_clean" || state == "sqlite_sidecar" {
				writePath = filepath.Join(owned, "conversations", session+".db")
			}
			if state == "no_session" {
				init = ""
			}
			local := process.Command{Path: "/bin/sh", Dir: cmd.Dir, Args: []string{"-c", `if [ -n "$1" ]; then printf fixture-state > "$1"; fi; printf '%s\n' "$2"; exec /bin/sleep 30`, "owned-AGY-stop-fixture", writePath, init}}
			if state == "sqlite_clean" || state == "sqlite_sidecar" {
				local.Args[1] = `umask 077; printf 'SQLite format 3\000%0112d' 0 > "$1"; if [ "$3" = sqlite_sidecar ]; then : > "$1-wal"; fi; printf '%s\n' "$2"; exec /bin/sleep 30`
				local.Args = append(local.Args, state)
			}
			var original *process.Handle
			meta := launchMetadata{commandID: g.CommandID, artifactGrant: metadataForLaunch(g).artifactGrant, completionMode: adapter.TaskCompletionV1, requireSession: true, outputProvider: "antigravity-cli", workRevision: 1, agyHistoryGrant: &g,
				afterWait: func(p *process.Handle, _ context.Context, _ error) { original = p }}
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			result, executeErr := h.execute(ctx, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, g.RunID, g.TaskID, local, false, meta)
			if original == nil {
				t.Fatalf("original Handle not observed: result=%s err=%v", result.Status, executeErr)
			}
			proofCtx, proofCancel := context.WithTimeout(context.Background(), time.Second)
			defer proofCancel()
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), time.Second)
				defer stop()
				if err := original.Stop(cleanup); err != nil {
					t.Errorf("owned original Handle cleanup unproved: %v", err)
				}
			})
			if err = original.Wait(proofCtx); err != nil {
				t.Fatal(err)
			}
			if err = original.ConfirmTreeExited(); err != nil {
				t.Fatal(err)
			}
			t.Logf("owned_fixture_cleanup original_wait=true group_absent=true pid=%d pgid=%d birth=%s", original.Identity().PID, original.Identity().PGID, original.Identity().Birth)
			spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
			if err != nil {
				t.Fatal(err)
			}
			events, err := spool.Read()
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			var sessionEvent, stopEvent contract.Event
			for _, e := range events {
				counts[e.Kind]++
				if e.Kind == contract.EventSession {
					sessionEvent = e
				} else if e.Kind == contract.EventStopped {
					stopEvent = e
				}
			}
			if result.Status != "interrupted" || counts[contract.EventStopped] != 1 || counts[contract.EventExited] != 1 || counts[contract.EventUnknown] != 0 {
				t.Fatalf("missing native history erased proved process facts: result=%s counts=%v err=%v", result.Status, counts, executeErr)
			}
			var seal agyHistorySeal
			sealErr := readContinuationMetadata(filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, agySealName), &seal)
			if state == "no_session" {
				if sealErr == nil || counts[contract.EventSession] != 0 {
					t.Fatal("Stop without accepted Session fabricated a resumable seal")
				}
				return
			}
			resume := g
			resume.SegmentID, resume.SessionKind, resume.SessionID = "second", "conversation-id", session
			resume.Continuation = &contract.ContinuationRef{Version: 1, Mode: contract.ContinuationInterrupted, SourceKind: "host", SourceSegmentID: g.SegmentID,
				SourceEventID: stopEvent.EventID, SourceEventRevision: stopEvent.Sequence, SourceEventSHA256: stopEvent.PayloadHash,
				SessionEventID: sessionEvent.EventID, AdapterPayloadSHA256: hashBytes(g.AdapterPayload), OwnerContextID: "owner"}
			_, resumeErr := h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "second"), resume)
			if (state == "present" || state == "sqlite_clean") && (sealErr != nil || resumeErr != nil || seal.Process.PID != original.Identity().PID) {
				t.Fatalf("proved Stop state was not sealed/resumable: seal=%v resume=%v", sealErr, resumeErr)
			} else if (state == "missing" || state == "sqlite_sidecar") && (sealErr == nil || resumeErr == nil) {
				t.Fatal("unsealed Stop state was admitted to continuation")
			}
			if state == "sqlite_clean" && (seal.Version != 2 || seal.StorageKind != "sqlite-v1") {
				t.Fatal("actual stopped SQLite actor was not bound to its explicit format")
			}
		})
	}
}

func TestAGYHistoryLifecycleUnknownNeverSealsCompleteNativeState(t *testing.T) {
	h, cmd, profile, g, originalApp := agyHistoryFixture(t)
	g.CommandID = "history-command-" + g.SegmentID
	g = managedHostGrant(t, h, g)
	if err := h.recordContinuationOrigin(g, nil); err != nil {
		t.Fatal(err)
	}
	prepared, err := h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "scratch"), g)
	if err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(filepath.Dir(originalApp), agyHistoryArgument(t, prepared))
	if err = os.Mkdir(filepath.Join(owned, "conversations"), 0700); err != nil {
		t.Fatal(err)
	}
	session := "9adeef5e-cd28-4c60-9858-a3462e408814"
	path := filepath.Join(owned, "conversations", session+".pb")
	init := `{"event":"init","conversation_id":"` + session + `"}`
	terminal := `{"event":"result","result":{"conversation_id":"` + session + `","status":"SUCCESS","response":"fixture commentary","structured_output":{"version":1,"kind":"technical_question","text":"fixture question"}}}`
	local := process.Command{Path: "/bin/sh", Dir: cmd.Dir, Args: []string{"-c", `printf complete-state > "$1"; printf '%s\n' "$2" "$3"`, "owned-AGY-first-canceled-stop", path, init, terminal}}
	var original *process.Handle
	meta := launchMetadata{commandID: g.CommandID, artifactGrant: metadataForLaunch(g).artifactGrant, completionMode: adapter.TaskCompletionV1, requireSession: true, outputProvider: "antigravity-cli", workRevision: 1, agyHistoryGrant: &g,
		afterWait: func(p *process.Handle, _ context.Context, waitErr error) {
			original = p
			if waitErr != nil {
				t.Errorf("causal fixture requires original Wait=nil: %v", waitErr)
				return
			}
			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			t.Logf("first_public_stop already_canceled=true err=%v", h.StopSegment(canceled, g.SegmentID))
		}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, executeErr := h.execute(ctx, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, g.RunID, g.TaskID, local, false, meta)
	if original != nil {
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			if err := original.Stop(cleanup); err != nil {
				t.Errorf("owned original Handle cleanup unproved: %v", err)
			}
		})
	}
	if original == nil || original.Wait(ctx) != nil || original.ConfirmTreeExited() != nil {
		t.Fatal("owned causal fixture original Wait/group absence not proved")
	}
	t.Logf("owned_fixture_cleanup original_wait=true group_absent=true pid=%d pgid=%d birth=%s", original.Identity().PID, original.Identity().PGID, original.Identity().Birth)
	spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	if err != nil {
		t.Fatal(err)
	}
	events, err := spool.Read()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, e := range events {
		counts[e.Kind]++
	}
	if result.Status != "unknown" || counts[contract.EventUnknown] != 1 || counts[contract.EventExited] != 0 || counts[contract.EventStopped] != 0 || counts[contract.EventQuestion] != 0 {
		t.Fatalf("canceled original Stop scope bypassed: result=%s counts=%v err=%v", result.Status, counts, executeErr)
	}
	if _, err = os.Lstat(filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, agySealName)); !os.IsNotExist(err) {
		t.Fatal("UNKNOWN path created a successful native seal")
	}
}
