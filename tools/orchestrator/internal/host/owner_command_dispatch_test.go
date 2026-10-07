package host

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

func ownerQueuedCommand(t *testing.T, permit contract.OwnerControlPermit, key ed25519.PrivateKey, kind, id string) []byte {
	t.Helper()
	var value any = contract.StopCommand{CommandID: id, SegmentID: "segment-" + id, Reason: "owner_stop_requested", DeadlineUnixMS: 123456789}
	if kind == "launch" {
		value = contract.LaunchCommand{CommandID: id, ReservationID: "reservation-" + id, RunID: "run", HostID: "producer", TaskID: "task-" + id, AttemptID: "attempt-" + id, SegmentID: "segment-" + id, WorkRevision: 1, PlanRevision: 1, ExecutionEpoch: permit.CoordinatorEpoch, GrantedActiveMS: 1000}
	}
	body, _ := json.Marshal(value)
	command, err := contract.SignOwnerCommand(contract.OwnerCommand{Version: 1, Permit: permit, Kind: kind, RequestID: id, Body: body}, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(command)
	return raw
}

func TestOwnerCommandQueuesBoundBothLanesAndAuthenticateBeforeOverload(t *testing.T) {
	for _, overflowKind := range []string{"launch", "stop"} {
		t.Run(overflowKind, func(t *testing.T) {
			cfg, permit, key := ownerExecutionFixture(t)
			var starts atomic.Int32
			cfg.invocation = func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
				starts.Add(1)
				return nil, errors.New("unexpected provider")
			}
			e, err := newOwnerExecution(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err = e.reconcile(context.Background(), permit); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			r := newOwnerRuntimeLifecycle(e, cancel)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			e.acceptGrant = func(grant contract.LaunchCommand) (bool, error) {
				once.Do(func() { close(entered) })
				<-release
				return cfg.host.acceptGrant(grant)
			}
			cfg.host.stopMu.Lock() // hold the original stop registry independently
			wantFailure := ""
			t.Cleanup(func() {
				cfg.host.stopMu.Unlock()
				joinCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				e.beginStop(joinCtx, context.Canceled)
				r.commands.close()
				close(release)
				cancel(context.Canceled)
				joined := e.join(joinCtx)
				if (wantFailure == "" && joined != nil) || (wantFailure != "" && (joined == nil || joined.Error() != wantFailure)) {
					t.Error("queue join lost original failure", joined)
				}
				for _, done := range []<-chan struct{}{r.commands.launchDone, r.commands.stopDone} {
					select {
					case <-done:
					case <-joinCtx.Done():
						t.Error("queued original command did not drain")
					}
				}
				if starts.Load() != 0 {
					t.Error("queued grant started after overload stop")
				}
			})
			submit := func(raw []byte, generation uint64) error {
				return e.commandScheduled(raw, func(job *ownerCommandJob) bool {
					job.generation = generation
					return r.commands.enqueue(job)
				})
			}
			for i := 0; i < 32; i++ {
				if err := submit(ownerQueuedCommand(t, permit, key, "launch", fmt.Sprintf("launch-%d", i)), 1); err != nil {
					t.Fatal("valid launch capacity rejected", i, err)
				}
				if err := submit(ownerQueuedCommand(t, permit, key, "stop", fmt.Sprintf("stop-%d", i)), 2); err != nil {
					t.Fatal("independent stop capacity rejected", i, err)
				}
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("launch lane did not enter admission")
			}
			for _, kind := range []string{"launch", "stop"} {
				if err := submit(ownerQueuedCommand(t, permit, key, kind, kind+"-0"), 9); err != nil {
					t.Fatal("exact replay consumed another slot", err)
				}
			}
			forged, _ := contract.DecodeOwnerCommand(ownerQueuedCommand(t, permit, key, overflowKind, "untrusted"))
			forged.Signature = strings.Repeat("0", len(forged.Signature))
			raw, _ := json.Marshal(forged)
			if err := submit(raw, 10); ownerCommandDisposition(err) != ownerCommandInvalid {
				t.Fatal("full queue bypassed signature verification", err)
			}
			e.mu.Lock()
			bounded := len(e.pendingCommands) == 64 && r.commands.launchPending == 32 && r.commands.stopPending == 32 && !e.stopping
			e.mu.Unlock()
			if !bounded || ctx.Err() != nil {
				t.Fatal("invalid/replayed commands changed capacity or authority")
			}
			wantFailure = "owner_control_queue_full"
			if err := submit(ownerQueuedCommand(t, permit, key, overflowKind, "overflow"), 11); ownerCommandDisposition(err) != ownerCommandLocalFailure || err.Error() != wantFailure {
				t.Fatal("new authenticated overload did not fail closed", err)
			}
			e.mu.Lock()
			bounded = len(e.pendingCommands) == 64 && r.commands.launchPending == 32 && r.commands.stopPending == 32 && e.stopping
			e.mu.Unlock()
			if !bounded || starts.Load() != 0 {
				t.Fatal("overload leaked local reservation or started a worker")
			}
			if _, err := os.Stat(filepath.Join(cfg.host.spoolRoot, ".grants")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("queue overflow claimed a durable admission/noStart it did not perform", err)
			}
		})
	}
}

func TestOwnerCommandQueuedGrantSurvivesGenerationAndEpochReplay(t *testing.T) {
	cfg, permit, key := ownerExecutionFixture(t)
	cfg.rpc = func(_ context.Context, _ uint64, kind, id string, _ any) (contract.OwnerRPCReply, error) {
		status := "applied"
		if kind == "activate" {
			status = "active"
		}
		raw, _ := json.Marshal(map[string]string{"status": status})
		return contract.OwnerRPCReply{Version: 1, OwnerID: permit.OwnerID, CoordinatorEpoch: permit.CoordinatorEpoch, RequestID: id, Body: raw}, nil
	}
	e, err := newOwnerExecution(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.reconcile(context.Background(), permit); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var admissions atomic.Int32
	e.acceptGrant = func(grant contract.LaunchCommand) (bool, error) {
		if admissions.Add(1) == 1 {
			close(entered)
		}
		<-release
		return cfg.host.acceptGrant(grant)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	r := newOwnerRuntimeLifecycle(e, cancel)
	a, b := ownerRuntimeControlPair(t)
	firstReader := r.readControl(ctx, 1, a)
	t.Cleanup(func() {
		joinCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		e.beginStop(joinCtx, context.Canceled)
		r.commands.close()
		close(release)
		cancel(context.Canceled)
		if err := e.join(joinCtx); err != nil {
			t.Error("original queued admission did not settle", err)
		}
		for _, done := range []<-chan struct{}{r.commands.launchDone, r.commands.stopDone} {
			select {
			case <-done:
			case <-joinCtx.Done():
				t.Error("old generation jobs did not join")
			}
		}
	})
	write := func(conn interface{ Write([]byte) (int, error) }, epoch uint64, kind, id string) {
		t.Helper()
		raw := ownerQueuedCommand(t, permit, key, kind, id)
		if err := ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerCommand, Epoch: epoch, RequestID: id, Payload: raw}); err != nil {
			t.Fatal(err)
		}
	}
	write(b, permit.CoordinatorEpoch, "launch", "original")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("original grant not admitted")
	}
	b.Close()
	select {
	case <-firstReader:
	case <-time.After(time.Second):
		t.Fatal("old reader blocked on admitted job")
	}
	oldHost := permit.NewHost
	permit.ExpectedControlRevision, permit.ControlRevision = 1, 2
	permit.CoordinatorEpoch++
	permit.OldHost, permit.RequestID, permit.PermitID = &oldHost, "renew", "permit-2"
	permit, err = contract.SignOwnerControlPermit(permit, key)
	if err != nil || e.reconcile(ctx, permit) != nil {
		t.Fatal("renew latest permit", err)
	}
	a2, b2 := ownerRuntimeControlPair(t)
	secondReader := r.readControl(ctx, 2, a2)
	write(b2, permit.CoordinatorEpoch, "launch", "original") // normalized business identity unchanged
	write(b2, permit.CoordinatorEpoch, "stop", "barrier")
	for deadline := time.Now().Add(time.Second); !cfg.host.stopWasRequested("segment-barrier"); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("new generation did not consume replay then stop")
		}
	}
	e.mu.Lock()
	unchanged := r.commands.launchPending == 1 && len(e.pendingCommands) <= 2
	e.mu.Unlock()
	if !unchanged || admissions.Load() != 1 || ctx.Err() != nil {
		t.Fatal("renewal duplicated or discarded old admitted grant", admissions.Load(), ctx.Err())
	}
	if err := e.commandScheduled(ownerQueuedCommand(t, permit, key, "launch", "original"), func(*ownerCommandJob) bool { t.Error("exact replay reached scheduler"); return false }); err != nil {
		t.Fatal(err)
	}
	cancel(context.Canceled)
	select {
	case <-secondReader:
	case <-time.After(time.Second):
		t.Fatal("new reader did not stop")
	}
}
