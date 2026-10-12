//go:build darwin

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Actual capture/FS and prelaunch rejection only. Synthetic durable source
// events do not establish original OS exit or terminal publisher behavior.
func TestCodexFinalCaptureObserverCancelAndPassiveUseOriginalVerification(t *testing.T) {
	for _, cancelAtBarrier := range []bool{false, true} {
		t.Run(map[bool]string{false: "passive", true: "cancel"}[cancelAtBarrier], func(t *testing.T) {
			h, p, g, bound := codexSealFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			spool, _ := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
			before, err := spool.Read()
			if err != nil {
				t.Fatal(err)
			}
			remaining := codexStateTotalLimit
			calls := 0
			candidate, err := h.captureManagedCodexQuestionSeal(ctx, p, g, bound, 1, hashText("question"), codexStateReadControl{Remaining: &remaining}, codexSealControl{BeforeFinalVerify: func() {
				calls++
				if remaining >= codexStateTotalLimit {
					t.Fatal("observer precedes actual state reads")
				}
				if _, e := os.Lstat(filepath.Join(p.Protected.Path, "codex-session-seal.json")); !errors.Is(e, os.ErrNotExist) {
					t.Fatal("observer follows publication")
				}
				if cancelAtBarrier {
					cancel()
				}
			}})
			if calls != 1 || remaining >= codexStateTotalLimit {
				t.Fatal("per-call observer or original shared read budget lost")
			}
			if cancelAtBarrier {
				if err == nil || !reflect.DeepEqual(candidate, codexQuestionSealCandidate{}) {
					t.Fatal("canceled original context bypassed final verification")
				}
			} else if err != nil || candidate.Record.QuestionRevision != 1 || len(candidate.Record.State.Slots) != 20 {
				t.Fatal("passive observer changed normal candidate", err)
			}
			after, readErr := spool.Read()
			if readErr != nil || !reflect.DeepEqual(before, after) || len(h.active) != 0 || len(h.codexLeases) != 0 {
				t.Fatal("pure candidate fabricated event/process/exit authority")
			}
			if _, e := os.Lstat(filepath.Join(p.Protected.Path, "codex-session-seal.json")); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("capture alone published a seal")
			}
		})
	}
}

func TestCodexFinalCaptureForeignControlRejectedBeforeLegacyReservation(t *testing.T) {
	h, g, _, _ := codexOriginFixture(t)
	g.ReservationID, g.GrantedActiveMS = "reservation", 60000
	called := false
	inv := launchInvocation{args: []string{"/owned/never-run"}, dir: filepath.Dir(h.spoolRoot)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.executeLaunch(ctx, g, inv, codexExecuteControl{beforeSealFinalVerify: func() { called = true }})
	if err == nil || err.Error() != "codex_actor_control_invalid" || called || len(h.reservations) != 0 || len(h.codexLeases) != 0 || len(h.active) != 0 {
		t.Fatal("foreign hook reached legacy preparation/reservation", err)
	}
	spool, _ := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	records, readErr := spool.Read()
	if readErr != nil || len(records) != 0 {
		t.Fatal("foreign hook produced legacy lifecycle records")
	}
}

func TestCodexFinalCaptureActorBarrierRequiresExplicitReleaseAndOriginalJoin(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer parentCancel()
	work, workCancel := context.WithCancel(parent)
	barrier := codexNewFinalCaptureBarrier()
	joined := make(chan struct{})
	go func() { barrier.observe(parent); close(joined) }()
	select {
	case <-barrier.ready:
	case <-parent.Done():
		t.Fatal("original readiness missing")
	}
	workCancel()
	if work.Err() == nil {
		t.Fatal("work cancellation fixture")
	}
	select {
	case <-joined:
		t.Fatal("cancellation bypassed explicit release; action observation could race terminal")
	default:
	}
	barrier.allow()
	barrier.allow()
	select {
	case <-joined:
	case <-parent.Done():
		t.Fatal("original observer not joined")
	}
	if barrier.calls.Load() != 1 {
		t.Fatal("per-call barrier count changed")
	}
}
