package host

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestOwnerTransportChildHelper(t *testing.T) {
	if os.Getenv("GO_OWNER_TRANSPORT_CHILD") != "1" {
		return
	}
	if marker := os.Getenv("GO_OWNER_FAST_EXIT_MARKER"); marker != "" {
		if err := os.WriteFile(marker, []byte("exiting"), 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	control, err := ipc.TakeOwnerControlFD(4)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	raw, err := ipc.ReadOwnerBootstrapPipe(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := contract.DecodeOwnerTransportBootstrap(raw)
	if err != nil || bootstrap.Owner.PID != os.Getppid() {
		t.Fatal("invalid public child bootstrap", err)
	}
	if os.Getenv("ORCHESTRATOR_CHILD_SECRET") != "" || os.Getenv("ORCHESTRATOR_ENABLE_TEST_FAKE") != "1" {
		t.Fatal("private environment escaped into transport")
	}
	if err = ipc.Write(control, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerHostPending, RequestID: "child-ready", Payload: json.RawMessage(`{"ready":true}`)}); err != nil {
		t.Fatal(err)
	}
	message, err := ipc.Read(control)
	if errors.Is(err, io.EOF) {
		return
	}
	if err != nil || message.RequestID != "release" {
		t.Fatal("missing explicit owner release", err)
	}
}

func TestOwnerTransportChildObservesBirthBeforeReapingFastExit(t *testing.T) {
	bootstrap, pending, environment := ownerTransportFixture(t)
	marker := filepath.Join(bootstrap.StateDir, "fast-exit-marker")
	environment = append(environment, "GO_OWNER_FAST_EXIT_MARKER="+marker)
	observedBeforeReap := false
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child, startErr := startOwnerTransportWith(ctx, bootstrap, pending, environment, []string{"-test.run=^TestOwnerTransportChildHelper$"}, func(ctx context.Context, pid int) (string, error) {
		for {
			if _, err := os.Stat(marker); err == nil {
				if quiet, err := process.GroupQuiescent(ctx, pid); err != nil {
					return "", err
				} else if quiet {
					break
				}
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}
		birth, err := process.BirthContext(ctx, pid)
		if err != nil {
			return "", err
		}
		if err = process.MatchWaitableGroupLeader(ctx, pid, birth); err != nil {
			return "", err
		}
		observedBeforeReap = true
		return birth, nil
	})
	if child == nil {
		t.Fatal("fast child lost original Start/Wait ownership", startErr)
	}
	defer child.control.Close()
	waitCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	err := child.wait(waitCtx)
	stop()
	if err != nil || !child.waited() {
		_ = child.command.Process.Kill()
		t.Fatal("fast child missing original Wait", err)
	}
	if !observedBeforeReap || child.identity.Birth == "" {
		t.Fatal("original child was reaped before birth observation", startErr)
	}
	if startErr == nil {
		t.Fatal("fast child closed bootstrap before write but Start was reported ready")
	}
	t.Logf("fast transport child: pid=%d birth_before_reap=true post_start_pipe_failure=true original_Wait=0", child.identity.PID)
}

func ownerTransportFixture(t *testing.T) (contract.OwnerTransportBootstrap, ownerRecoveryPending, []string) {
	t.Helper()
	cfg, _, _ := ownerExecutionFixture(t)
	scope := cfg.binding.Scope
	scope.LaunchID = scope.ProducerID
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	scope.Executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	scope.ExecutableSHA256, err = process.ExecutableDigest(context.Background(), scope.Executable)
	if err != nil {
		t.Fatal(err)
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	owner := contract.OriginalOwnerIdentity{PID: os.Getpid(), Birth: birth, PublicKey: hex.EncodeToString(cfg.privateKey.Public().(ed25519.PublicKey))}
	intent := contract.OriginalOwnerIntent{Scope: scope, BootstrapID: "bootstrap", BootstrapTokenSHA256: strings.Repeat("a", 64), CoordinatorKeyID: cfg.binding.CoordinatorKeyID, RequestID: "prepare", RequestSHA256: strings.Repeat("b", 64), StartupDeadlineUnixMS: time.Now().Add(time.Minute).UnixMilli(), FirstHost: cfg.binding.FirstHost, CoordinatorEpoch: 7}
	bootstrap := contract.OwnerTransportBootstrap{Version: 1, StateDir: t.TempDir(), Intent: intent, Owner: owner, CoordinatorPublicKey: hex.EncodeToString(cfg.coordinatorKey)}
	if err := os.Chmod(bootstrap.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Validate(); err != nil {
		t.Fatal("invalid native transport fixture", err)
	}
	pending := ownerRecoveryPending{StartSlot: 1, RequestID: "replacement", ExpectedControlRevision: 1, OldHost: cfg.binding.FirstHost, Phase: "starting"}
	environment := append(os.Environ(), "GO_OWNER_TRANSPORT_CHILD=1", "ORCHESTRATOR_CHILD_SECRET=must-not-cross", "ORCHESTRATOR_ENABLE_TEST_FAKE=1")
	return bootstrap, pending, environment
}

func TestOwnerTransportChildRetainsOriginalWaitAfterBirthFailure(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		name := "known_birth"
		if unknown {
			name = "post_start_birth_failure"
		}
		t.Run(name, func(t *testing.T) {
			bootstrap, pending, environment := ownerTransportFixture(t)
			birthLookup := process.BirthContext
			if unknown {
				birthLookup = func(context.Context, int) (string, error) {
					return "", errors.New("injected birth observation failure")
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			child, err := startOwnerTransportWith(ctx, bootstrap, pending, environment, []string{"-test.run=^TestOwnerTransportChildHelper$"}, birthLookup)
			cancel()
			if child == nil {
				t.Fatal("successful native Start lost its original owner", err)
			}
			t.Cleanup(func() {
				_ = child.control.Close()
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := child.wait(ctx); err != nil {
					_ = child.command.Process.Kill()
					killed, stop := context.WithTimeout(context.Background(), 2*time.Second)
					_ = child.wait(killed)
					stop()
					t.Error("helper required forced cleanup", err)
				}
			})
			if (err != nil) != unknown || (child.identity.Birth == "") != unknown || child.identity.PID <= 0 {
				t.Fatal("post-Start classification lost actual child ownership", err)
			}
			if err := child.control.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			ready, err := ipc.Read(child.control)
			if err != nil || ready.RequestID != "child-ready" {
				t.Fatal("native child did not receive FD3/FD4 bootstrap", err)
			}
			short, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			if err = child.wait(short); !errors.Is(err, context.DeadlineExceeded) || child.waited() {
				t.Fatal("launch context cancellation stopped child or fabricated Wait", err)
			}
			cancel()
			if child.reservation.RequestID != pending.RequestID || child.reservation.StartSlot != pending.StartSlot {
				t.Fatal("child detached from original recovery reservation")
			}
			scope, err := originalOwnerRecoveryScope(bootstrap.Intent.Scope, bootstrap.Owner)
			if err != nil || child.recoveryScope != scope {
				t.Fatal("child detached from original owner scope", err)
			}
			if err = ipc.Write(child.control, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerCommand, RequestID: "release", Payload: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
			waitCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
			err = child.wait(waitCtx)
			stop()
			if err != nil || !child.waited() || child.command.ProcessState == nil || child.command.ProcessState.ExitCode() != 0 {
				t.Fatal("original child Wait proof missing", err)
			}
			t.Logf("original transport child Wait: pid=%d exit=0 birth_known=%t forced_cleanup=false", child.identity.PID, !unknown)
		})
	}
}

func TestOwnerTransportChildRejectsBeforeStart(t *testing.T) {
	bootstrap, pending, environment := ownerTransportFixture(t)
	for _, kind := range []string{"reservation", "digest", "canceled", "foreign_owner"} {
		t.Run(kind, func(t *testing.T) {
			b, p := bootstrap, pending
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "reservation":
				p.Phase = "reserved"
			case "digest":
				b.Intent.Scope.ExecutableSHA256 = strings.Repeat("0", 64)
			case "canceled":
				cancel()
			case "foreign_owner":
				b.Owner.PID++
			}
			if child, err := startOwnerTransportWith(ctx, b, p, environment, []string{"-test.run=^TestOwnerTransportChildHelper$"}, process.BirthContext); err == nil || child != nil {
				t.Fatal("invalid pre-Start authority created a child")
			}
		})
	}
}
