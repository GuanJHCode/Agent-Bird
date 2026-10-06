//go:build darwin

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Actual helper codec/FS barrier -> M1/M3 -> Host callback. It contains only
// synthetic process rows and net.Pipe; no Start, Wait, signal or sandbox runs.
func TestCodexActorProtocolFramesTraverseRealM3WithoutOS(t *testing.T) {
	for _, scenario := range []string{"question", "result", "malformed", "permission"} {
		t.Run(scenario, func(t *testing.T) {
			h, g, inv, env := codexOriginFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			p, err := h.prepareManagedCodexState(ctx, g, inv, env, codexPreparationControl{})
			if err != nil {
				t.Fatal(err)
			}
			_, p, err = codexManagedCommand(ctx, p, env, h.spoolRoot)
			if err != nil {
				t.Fatal(err)
			}
			id := process.Identity{PID: 12345, PGID: 12345, Birth: "synthetic-only", BirthKnown: true, Executable: p.Launch.Path, ExecutableSHA256: p.Launch.SHA256}
			spool, _ := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
			meta := launchMetadata{commandID: g.CommandID, executionEpoch: g.ExecutionEpoch, workRevision: g.WorkRevision}
			for _, kind := range []string{contract.EventSpawned, contract.EventRunning} {
				if _, err := h.appendPhaseEvent(spool, g.RunID, g.TaskID, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, meta, kind, hashText(kind), &id, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			local, remote := net.Pipe()
			client, err := codexrpc.New(ctx, local, codexrpc.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			protocolDone := make(chan error, 1)
			go func() {
				defer remote.Close()
				protocolDone <- codexActorProtocol(ctx, remote, remote, p.Layout.Home.Path, p.Origin.Workspace, scenario, id.PID)
			}()
			type returned struct {
				out   codexsession.Outcome
				bound codexBoundThread
				err   error
			}
			runDone := make(chan returned, 1)
			go func() {
				out, bound, err := h.runManagedCodexTurn(ctx, client, p, g, id)
				runDone <- returned{out, bound, err}
			}()
			gotRun, gotProtocol := false, false
			defer func() {
				cancel()
				_ = remote.Close()
				_ = client.Close()
				if !gotRun {
					<-runDone
				}
				if !gotProtocol {
					<-protocolDone
				}
			}()
			if codexActorAwaitMarker(ctx, p.Layout.Home.Path, id.PID, "initialize-ready") != nil || codexActorWriteMarker(p.Layout.Home.Path, id.PID, "initialize-release") != nil {
				t.Fatal("shared initialize barrier")
			}
			if codexActorAwaitMarker(ctx, p.Layout.Home.Path, id.PID, "turn-ready") != nil {
				// Closed errors only; no frame/content/account values escape.
				select {
				case value := <-runDone:
					gotRun = true
					t.Log("consumer-before-turn", value.err)
				default:
				}
				select {
				case value := <-protocolDone:
					gotProtocol = true
					t.Log("helper-before-turn", value)
				default:
				}
				t.Fatal("shared turn barrier")
			}
			events, err := spool.Read()
			if err != nil || len(events) != 3 || events[2].Kind != contract.EventSession {
				t.Fatal("actual helper turn preceded durable Session")
			}
			if codexActorWriteMarker(p.Layout.Home.Path, id.PID, "turn-release") != nil {
				t.Fatal("shared turn release")
			}
			value := <-runDone
			gotRun = true
			protocolErr := <-protocolDone
			gotProtocol = true
			if protocolErr != nil {
				t.Fatal("shared helper frame shape or close behavior", protocolErr)
			}
			if value.bound.Session.EventID == "" || value.out.Binding.ThreadID != codexActorThreadID {
				t.Fatal("shared codec skipped original callback")
			}
			switch scenario {
			case "permission":
				if !errors.Is(value.err, codexsession.ErrPermission) || value.out.Completion.Kind != "" {
					t.Fatal("shared denial became completion", value.err)
				}
			case "malformed":
				if !errors.Is(value.err, codexsession.ErrProtocol) || value.out.Completion.Kind != "" {
					t.Fatal("shared malformed frame became completion", value.err)
				}
			default:
				kind := "technical_question"
				if scenario == "result" {
					kind = "result"
				}
				if value.err != nil || value.out.Status != "completed" || value.out.Completion.Kind != kind {
					t.Fatal("shared completion contract", value.err)
				}
				candidate, err := h.captureManagedCodexQuestionSeal(ctx, p, g, value.bound, 1, hashText(value.out.Completion.Text), codexStateReadControl{}, codexSealControl{})
				if err != nil || len(candidate.Record.State.Slots) != 20 {
					t.Fatal("shared opaque state not sealable", err)
				}
				if _, err := os.Lstat(filepath.Join(p.Protected.Path, "codex-session-seal.json")); !os.IsNotExist(err) {
					t.Fatal("pure codec manufactured original-exit publication")
				}
			}
			select {
			case <-client.Done():
			default:
				t.Fatal("real M1 reader/writer not joined")
			}
		})
	}
}

func TestCodexActorHelperDiagnosticKeepsClosedPhaseAndNoContent(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	if os.MkdirAll(filepath.Join(home, "tmp"), 0700) != nil {
		t.Fatal("closed diagnostic fixture")
	}
	const secret = "SYNTHETIC_RPC_ID_KEY_AND_VALUE_SENTINEL"
	raw := []byte(`{"id":"` + secret + `","method":"` + secret + `","params":{"` + secret + `":"` + secret + `"}}` + "\n")
	var output bytes.Buffer
	if !errors.Is(codexActorProtocol(context.Background(), bytes.NewReader(raw), &output, home, base, "question", 1234), errCodexStateFile) || output.Len() != 0 {
		t.Fatal("invalid initial method was accepted")
	}
	body, err := os.ReadFile(codexActorMarker(home, 1234, "helper-failure"))
	if err != nil || len(body) > 128 || strings.Contains(string(body), secret) {
		t.Fatal("helper diagnosis leaks or exceeds its closed shape")
	}
	var record map[string]string
	if json.Unmarshal(body, &record) != nil || len(record) != 2 || record["phase"] != "initialize" || record["error_class"] != "fixture_state" {
		t.Fatal("helper error attributed to a different native phase")
	}
	if codexActorErrorClass(&os.PathError{Op: secret, Path: secret, Err: errors.New(secret)}) != "filesystem" || codexActorErrorClass(errors.New(secret)) != "other" {
		t.Fatal("error class expanded beyond closed labels")
	}
}

func TestCodexActorMarkerIsCompleteAndNeverReplacesExistingRelease(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	if os.MkdirAll(filepath.Join(home, "tmp"), 0700) != nil {
		t.Fatal("marker fixture")
	}
	if codexActorWriteMarker(home, 1234, "turn-release") != nil {
		t.Fatal("atomic marker publication")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if codexActorAwaitMarker(ctx, home, 1234, "turn-release") != nil {
		t.Fatal("published marker is partial/unsafe")
	}
	path := codexActorMarker(home, 1234, "turn-release")
	before, _ := os.Lstat(path)
	if codexActorWriteMarker(home, 1234, "turn-release") == nil {
		t.Fatal("duplicate release replaced original")
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("original release identity changed")
	}
	if codexActorAwaitMarker(ctx, home, 1234, "turn-release") != nil {
		t.Fatal("duplicate modified original release bytes")
	}
}

func TestCodexActorExecutionBudgetReservesOriginalCleanupRemainder(t *testing.T) {
	now := time.Unix(100, 0)
	for _, remaining := range []time.Duration{40 * time.Second, 30 * time.Second, 20 * time.Second, 12 * time.Second, 11 * time.Second, time.Second, 0} {
		t.Run(remaining.String(), func(t *testing.T) {
			parent := now.Add(remaining)
			deadline, err := codexActorExecutionDeadline(parent, now)
			if remaining <= 11*time.Second {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("exhausted cleanup reserve still permits actor Start")
				}
				return
			}
			if err != nil || !deadline.After(now) || deadline.After(now.Add(20*time.Second)) || deadline.After(parent.Add(-11*time.Second)) {
				t.Fatal("execution consumed original Stop/confirmation reserve")
			}
		})
	}
}
