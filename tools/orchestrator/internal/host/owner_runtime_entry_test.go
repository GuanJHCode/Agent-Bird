package host

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

// The public runtime entry must reject before it creates spool state when its
// inherited control endpoint is absent.  This is the first boundary exercised
// by the CLI before it accepts any owner bootstrap.
func TestRunOriginalOwnerRejectsMissingControl(t *testing.T) {
	fixture, _, _ := ownerEnrollmentFixture(t)
	if err := RunOriginalOwner(context.Background(), OriginalOwnerConfig{Bootstrap: fixture.bootstrap}); err == nil {
		t.Fatal("runtime accepted missing inherited control endpoint")
	}
}

func TestOwnerRuntimeRecoveryRetainsPostStartFailureUntilOriginalWait(t *testing.T) {
	bootstrap, _, environment := ownerTransportFixture(t)
	executionCfg, current, coordinatorKey := ownerExecutionFixture(t)
	bootstrap.Owner.PublicKey = hex.EncodeToString(executionCfg.privateKey.Public().(ed25519.PublicKey))
	bootstrap.CoordinatorPublicKey = hex.EncodeToString(executionCfg.coordinatorKey)
	bootstrap.Intent.CoordinatorKeyID = executionCfg.binding.CoordinatorKeyID
	executionCfg.binding.Scope = bootstrap.Intent.Scope
	executionCfg.binding.Owner = bootstrap.Owner
	executionCfg.binding.BindingSHA256 = ""
	var err error
	executionCfg.binding, err = contract.SignOriginalOwnerBinding(executionCfg.binding, coordinatorKey)
	if err != nil {
		t.Fatal(err)
	}
	current.ScopeSHA256, _ = contract.OriginalOwnerScopeSHA256(executionCfg.binding.Scope)
	current.OwnerIdentitySHA256, _ = contract.OriginalOwnerIdentitySHA256(bootstrap.Owner)
	current, err = contract.SignOwnerControlPermit(current, coordinatorKey)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	execution, err := newOwnerExecution(ctx, executionCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer execution.run.cancel(context.Canceled)
	lifecycle := newOwnerRuntimeLifecycle(execution, cancel)
	scope, err := originalOwnerRecoveryScope(bootstrap.Intent.Scope, bootstrap.Owner)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := openOwnerRecovery(privateRecoveryTestDirectory(t), scope)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.close()
	cfg := OriginalOwnerConfig{Bootstrap: contract.OwnerRuntimeBootstrap{StateDir: bootstrap.StateDir, Prepared: contract.OwnerPrepared{Intent: bootstrap.Intent, CoordinatorPublicKey: bootstrap.CoordinatorPublicKey}}}
	var child *ownerTransportChild
	defer func() {
		if child != nil {
			_ = child.control.Close()
			joined, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			_ = child.wait(joined)
			if !child.waited() {
				_ = child.command.Process.Kill()
				reap, stopReap := context.WithTimeout(context.Background(), 2*time.Second)
				defer stopReap()
				_ = child.wait(reap)
				t.Error("post-Start child required forced test cleanup")
			}
		}
	}()
	start := func(ctx context.Context, b contract.OwnerTransportBootstrap, pending ownerRecoveryPending, _ []string) (*ownerTransportChild, error) {
		var startErr error
		child, startErr = startOwnerTransportWith(ctx, b, pending, environment, []string{"-test.run=^TestOwnerTransportChildHelper$"}, func(context.Context, int) (string, error) {
			return "", errors.New("injected post-Start birth observation failure")
		})
		if child == nil || startErr == nil || child.identity.Birth != "" {
			t.Fatal("fixture did not create an owned child with unknown birth", startErr)
		}
		if err := child.control.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		ready, err := ipc.Read(child.control)
		if err != nil || ready.RequestID != "child-ready" || child.waited() {
			t.Fatal("post-Start child was not alive before runtime cleanup", err)
		}
		return child, startErr
	}
	transport := &ownerRuntimeTransport{generation: 1, identity: current.NewHost}
	err = recoverDeadOwnerTransportWithStart(ctx, cfg, nil, execution, lifecycle, recovery, transport, current, start)
	if err == nil || child == nil {
		t.Fatal("recovery did not propagate post-Start failure", err)
	}
	if recovery.record.Attempts != 1 || recovery.record.OwnerPending == nil || recovery.record.OwnerPending.Phase != "starting" || recovery.record.OwnerPending.NewHost != nil {
		t.Fatal("post-Start uncertainty lost reservation or invented identity")
	}
	if _, retryErr := recovery.reserveTransport(time.Now(), "second-start", current.ControlRevision, current.NewHost); retryErr == nil {
		t.Fatal("post-Start uncertainty allowed another Start")
	}
	cleanupErr := lifecycle.cleanup(err, nil)
	if !child.waited() {
		t.Fatal("runtime cleanup lost original post-Start child Wait")
	}
	var waitErr *exec.ExitError
	if !errors.As(cleanupErr, &waitErr) || !errors.Is(cleanupErr, child.waitError) {
		t.Fatal("cleanup did not preserve original child Wait failure", cleanupErr)
	}
	status, ok := child.command.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatal("owned child did not exit from runtime cleanup SIGTERM", status)
	}
	if !errors.Is(syscall.Kill(child.identity.PID, 0), syscall.ESRCH) {
		t.Fatal("post-Start child was not reaped")
	}
	t.Log("recovery retained unknown-birth child; cleanup SIGTERM joined its original Wait without another Start")
}
