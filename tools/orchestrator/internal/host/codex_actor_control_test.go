package host

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Builder/hash reads only; no sandbox/helper executable is run by these tests.
func TestCodexActorTailPreservesRealBuilderScopeAndChecksActualHelper(t *testing.T) {
	h, g, inv, env := codexOriginFixture(t)
	p, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
	if err != nil {
		t.Fatal(err)
	}
	cmd, p, err := codexManagedCommand(context.Background(), p, env, h.spoolRoot)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := codexActorTail(context.Background(), cmd, p, codexActorCommand{})
	if err != nil || !reflect.DeepEqual(cmd, unchanged) {
		t.Fatal("zero public control changed builder")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "owned-helper")
	body := []byte("synthetic executable bytes; never execute")
	if os.WriteFile(path, body, 0700) != nil {
		t.Fatal("helper fixture")
	}
	a := codexActorCommand{Path: path, SHA256: codexDigest(body), Scenario: "question"}
	replaced, err := codexActorTail(context.Background(), cmd, p, a)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Path != cmd.Path || replaced.PinnedPath != cmd.PinnedPath || replaced.PinnedSHA256 != cmd.PinnedSHA256 || replaced.Dir != cmd.Dir || replaced.ExactEnv != cmd.ExactEnv || !reflect.DeepEqual(replaced.Env, cmd.Env) || replaced.Args[0] != cmd.Args[0] || replaced.Args[1] != cmd.Args[1] || replaced.Args[2] != path || cmd.Args[2] != cmd.PinnedPath {
		t.Fatal("actor altered scope/env/sandbox/provider pin or original argv")
	}
	for _, kind := range []string{"helper_hash", "scenario", "provider_pin", "provider_path", "argv", "helper_drift"} {
		t.Run(kind, func(t *testing.T) {
			badCommand := cmd
			badCommand.Args = append([]string(nil), cmd.Args...)
			badActor := a
			switch kind {
			case "helper_hash":
				badActor.SHA256 = p.Origin.Lock.Binary.SHA256
			case "scenario":
				badActor.Scenario = "arbitrary"
			case "provider_pin":
				badCommand.PinnedSHA256 = codexDigest([]byte("other"))
			case "provider_path":
				badCommand.PinnedPath = path
			case "argv":
				badCommand.Args[3] = "exec"
			case "helper_drift":
				if os.WriteFile(path, []byte("replacement executable bytes"), 0700) != nil {
					t.Fatal("helper drift fixture")
				}
			}
			if _, err := codexActorTail(context.Background(), badCommand, p, badActor); err == nil {
				t.Fatal("wrong actual helper/builder admitted")
			}
		})
	}
}

func TestCodexActorBeforeStartCanCancelSamePrivateLaunchWithoutProcessProof(t *testing.T) {
	h, g, inv, env := codexOriginFixture(t)
	for _, entry := range env {
		if len(entry) >= 5 && entry[:5] == "HOME=" {
			t.Setenv("HOME", entry[5:])
		}
		if len(entry) >= 11 && entry[:11] == "CODEX_HOME=" {
			t.Setenv("CODEX_HOME", entry[11:])
		}
	}
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL"} {
		t.Setenv(key, "")
	}
	g.ReservationID, g.GrantedActiveMS = "reservation", 60000
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	reached := false
	result, err := h.executeLaunch(ctx, g, inv, codexExecuteControl{beforeStart: func() {
		reached = true
		stopCtx, stopCancel := context.WithTimeout(ctx, time.Second)
		defer stopCancel()
		if err := h.StopSegment(stopCtx, g.SegmentID); err != nil {
			t.Fatal(err)
		}
	}})
	if !reached || err == nil || result.Status != "failed" || len(h.codexLeases) != 0 || len(h.active) != 0 {
		t.Fatal("beforeStart did not cancel original reserved execution without Start", reached, err)
	}
	spool, _ := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	events, err := spool.Read()
	if err != nil || len(events) != 2 || events[0].Kind != contract.EventPrepared || events[1].Kind != contract.EventLaunchRejected || events[1].Process != nil {
		t.Fatal("private seam fabricated process/Session/exit proof")
	}
}
