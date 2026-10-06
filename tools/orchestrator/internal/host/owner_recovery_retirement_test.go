package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func recoveryForTransportTest(t *testing.T, bootstrap contract.OwnerTransportBootstrap, pending ownerRecoveryPending) *ownerRecoveryState {
	t.Helper()
	scope, err := originalOwnerRecoveryScope(bootstrap.Intent.Scope, bootstrap.Owner)
	if err != nil {
		t.Fatal(err)
	}
	s, err := openOwnerRecovery(privateRecoveryTestDirectory(t), scope)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	if _, err = s.reserveTransport(time.UnixMilli(100_000), pending.RequestID, pending.ExpectedControlRevision, pending.OldHost); err != nil {
		t.Fatal(err)
	}
	return s
}

func rejectRetirementWithoutWrite(t *testing.T, s *ownerRecoveryState, action func() error) {
	t.Helper()
	before, err := os.ReadFile(s.file.path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.file.path)
	if err != nil {
		t.Fatal(err)
	}
	if err = action(); err == nil {
		t.Fatal("unproved retirement accepted")
	}
	after, err := os.ReadFile(s.file.path)
	if err != nil {
		t.Fatal(err)
	}
	newInfo, err := os.Stat(s.file.path)
	if err != nil || !os.SameFile(info, newInfo) || !bytes.Equal(before, after) {
		t.Fatal("rejected retirement wrote ledger", err)
	}
}

func TestOriginalOwnerRecoveryRetiresKnownNoStart(t *testing.T) {
	for _, phase := range []string{"reserved", "starting"} {
		t.Run(phase, func(t *testing.T) {
			bootstrap, pending, environment := ownerTransportFixture(t)
			s := recoveryForTransportTest(t, bootstrap, pending)
			if phase == "starting" {
				if err := s.beginTransportStart(); err != nil {
					t.Fatal(err)
				}
			}
			pending = *s.record.OwnerPending
			before := s.copyRecord()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			child, startErr := startOwnerTransportWith(ctx, bootstrap, pending, environment, nil, process.BirthContext)
			if child != nil || !errors.Is(startErr, context.Canceled) {
				t.Fatal("expected known no Start", startErr)
			}
			rejectRetirementWithoutWrite(t, s, func() error { return s.retireTransportNoStart(errors.New("not launcher evidence")) })
			for _, mutation := range []string{"scope", "slot", "request", "revision", "old", "phase"} {
				t.Run(mutation, func(t *testing.T) {
					b, p := bootstrap, pending
					switch mutation {
					case "scope":
						b.Owner.PID++
					case "slot":
						p.StartSlot++
					case "request":
						p.RequestID += "-other"
					case "revision":
						p.ExpectedControlRevision++
					case "old":
						p.OldHost.PID++
					case "phase":
						if p.Phase == "reserved" {
							p.Phase = "starting"
						} else {
							p.Phase = "reserved"
						}
					}
					_, wrong := startOwnerTransportWith(ctx, b, p, environment, nil, process.BirthContext)
					rejectRetirementWithoutWrite(t, s, func() error { return s.retireTransportNoStart(wrong) })
				})
			}
			if err := s.retireTransportNoStart(fmt.Errorf("launch rejected: %w", startErr)); err != nil {
				t.Fatal("known noStart could not retire", err)
			}
			if s.record.OwnerPending != nil || s.record.Reservation != nil || s.record.Attempts != before.Attempts || s.record.NextAttemptUnixMS != before.NextAttemptUnixMS {
				t.Fatal("retirement refunded or retained reservation")
			}
			rejectRetirementWithoutWrite(t, s, func() error { return s.retireTransportNoStart(startErr) })
			s.close()
			reopened, err := openOwnerRecovery(s.file.directory, s.record.ScopeSHA256)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.close()
			if _, err = reopened.reserveTransport(time.UnixMilli(200_000), "next", 1, pending.OldHost); err != nil || reopened.record.Attempts != 2 {
				t.Fatal("restart refunded noStart budget", err)
			}
			rejectRetirementWithoutWrite(t, reopened, func() error { return reopened.retireTransportNoStart(startErr) })
		})
	}
}

func TestOriginalOwnerRecoveryRetiresOnlyOriginalWait(t *testing.T) {
	for _, kind := range []string{"starting", "unknown", "started", "coordinator", "exhausted", "poison"} {
		t.Run(kind, func(t *testing.T) {
			bootstrap, pending, environment := ownerTransportFixture(t)
			s := recoveryForTransportTest(t, bootstrap, pending)
			if err := s.beginTransportStart(); err != nil {
				t.Fatal(err)
			}
			birth := process.BirthContext
			if kind == "unknown" {
				birth = func(context.Context, int) (string, error) { return "", errors.New("injected unknown birth") }
			}
			child, startErr := startOwnerTransportWith(context.Background(), bootstrap, pending, environment, []string{"-test.run=^TestOwnerTransportChildHelper$"}, birth)
			if child == nil {
				t.Fatal("lost native child", startErr)
			}
			t.Cleanup(func() {
				_ = child.control.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if err := child.wait(ctx); err != nil {
					_ = child.command.Process.Kill()
					reap, stop := context.WithTimeout(context.Background(), time.Second)
					_ = child.wait(reap)
					stop()
					t.Error("forced helper cleanup", err)
				}
			})
			if err := child.control.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			ready, err := ipc.Read(child.control)
			if err != nil || ready.RequestID != "child-ready" {
				t.Fatal("native child did not finish bootstrap", err)
			}
			if kind == "unknown" {
				if err := s.markTransportUnknown(); err != nil {
					t.Fatal(err)
				}
			} else if kind != "starting" {
				if err := s.recordTransportStarted(child.identity); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "coordinator" {
				if _, err := s.reserveCoordinator(time.UnixMilli(200_000)); err != nil {
					t.Fatal(err)
				}
				if err := s.beginCoordinatorEnsure(); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "exhausted" {
				for i := 2; i <= 5; i++ {
					if _, err := s.reserveCoordinator(time.UnixMilli(int64(i) * 100_000)); err != nil {
						t.Fatal(err)
					}
					if err := s.beginCoordinatorEnsure(); err != nil {
						t.Fatal(err)
					}
					if err := s.finishCoordinatorEnsure(); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := s.reserveCoordinator(time.UnixMilli(600_000)); !errors.Is(err, errCoordinatorUnavailable) {
					t.Fatal(err)
				}
			}
			rejectRetirementWithoutWrite(t, s, func() error { return s.retireTransportAfterWait(child) })
			rejectRetirementWithoutWrite(t, s, func() error { return s.retireTransportAfterWait(nil) })
			rejectRetirementWithoutWrite(t, s, func() error { return s.retireTransportAfterWait(&ownerTransportChild{identity: child.identity}) })
			rejectRetirementWithoutWrite(t, s, func() error { return s.retireTransportNoStart(startErr) })
			_ = child.control.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err = child.wait(ctx)
			cancel()
			if err != nil || !child.waited() {
				t.Fatal("original native Wait unavailable", err)
			}
			for _, mutation := range []string{"scope", "slot", "request", "revision", "old", "identity"} {
				t.Run(mutation, func(t *testing.T) {
					wrong := *child
					switch mutation {
					case "scope":
						wrong.recoveryScope = "wrong"
					case "slot":
						wrong.reservation.StartSlot++
					case "request":
						wrong.reservation.RequestID += "-other"
					case "revision":
						wrong.reservation.ExpectedControlRevision++
					case "old":
						wrong.reservation.OldHost.PID++
					case "identity":
						wrong.identity.PID++
					}
					rejectRetirementWithoutWrite(t, s, func() error { return s.retireTransportAfterWait(&wrong) })
				})
			}
			if kind == "poison" {
				before, err := os.ReadFile(s.file.path)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(s.file.path, append(append([]byte(nil), before...), ' '), 0600); err != nil {
					t.Fatal(err)
				}
				if err = s.retireTransportAfterWait(child); err == nil || !s.failed {
					t.Fatal("changed durable state did not poison retirement", err)
				}
				if err = os.WriteFile(s.file.path, before, 0600); err != nil {
					t.Fatal(err)
				}
				rejectRetirementWithoutWrite(t, s, func() error { return s.retireTransportAfterWait(child) })
				return
			}
			before := s.copyRecord()
			if err := s.retireTransportAfterWait(child); err != nil {
				t.Fatal("original Wait could not retire", err)
			}
			if s.record.OwnerPending != nil || s.record.Attempts != before.Attempts || s.record.NextAttemptUnixMS != before.NextAttemptUnixMS || s.record.Unavailable != before.Unavailable {
				t.Fatal("retirement lost budget/terminal state")
			}
			if kind == "coordinator" {
				if s.record.Reservation == nil || *s.record.Reservation != *before.Reservation {
					t.Fatal("retirement erased independent Ensure")
				}
				if _, err := s.reserveTransport(time.UnixMilli(700_000), "too-soon", 1, pending.OldHost); err == nil {
					t.Fatal("ongoing Ensure permitted Start")
				}
				if err := s.finishCoordinatorEnsure(); err != nil {
					t.Fatal(err)
				}
			} else if s.record.Reservation != nil {
				t.Fatal("retired child kept transport reservation")
			}
			rejectRetirementWithoutWrite(t, s, func() error { return s.retireTransportAfterWait(child) })
			s.close()
			reopened, err := openOwnerRecovery(s.file.directory, s.record.ScopeSHA256)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.close()
			_, err = reopened.reserveTransport(time.UnixMilli(700_000), "next-child", 1, pending.OldHost)
			if kind == "exhausted" {
				if !errors.Is(err, errCoordinatorUnavailable) || !reopened.record.Unavailable {
					t.Fatal("Wait reset exhausted budget", err)
				}
			} else if err != nil || reopened.record.Attempts != before.Attempts+1 {
				t.Fatal("Wait refunded Start budget", err)
			}
			t.Logf("retired original native child: pid=%d phase=%s original_Wait=0 attempts_before=%d", child.identity.PID, kind, before.Attempts)
		})
	}
}
