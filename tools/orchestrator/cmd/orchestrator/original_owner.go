package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitopsworker"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

var initialOwnerStatusTimeout = 5 * time.Second

// originalOwnerEntry accepts only the two inherited descriptors created by the
// initial transport launcher.  In particular it has no token, key, socket or
// recovery arguments: those would make the private owner re-creatable outside
// the prepared transport chain.
func originalOwnerEntry(parent context.Context, args []string) error {
	fs := flag.NewFlagSet("original-owner", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	bootstrapFD := fs.Int("bootstrap-fd", -1, "inherited owner bootstrap")
	controlFD := fs.Int("control-fd", -1, "inherited owner control")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *bootstrapFD != 3 || *controlFD != 4 {
		return codeError("invalid_args")
	}
	ctx, cancel := signal.NotifyContext(parent, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	control, err := ipc.TakeOwnerControlFD(*controlFD)
	if err != nil {
		return err
	}
	defer control.Close()
	raw, err := ipc.ReadOwnerBootstrapPipe(ctx, *bootstrapFD)
	if err != nil {
		return err
	}
	bootstrap, err := contract.DecodeOwnerRuntimeBootstrap(raw)
	if err != nil {
		return err
	}
	return host.RunOriginalOwner(ctx, host.OriginalOwnerConfig{
		Bootstrap: bootstrap,
		Control:   control,
		InvocationProvider: func(ctx context.Context, grant contract.LaunchCommand) (contract.InvocationView, error) {
			var discriminator struct {
				Kind string `json:"kind"`
			}
			if json.Unmarshal(grant.AdapterPayload, &discriminator) == nil && discriminator.Kind == "gitops" {
				return gitopsworker.BuildInvocation(grant.AdapterPayload, bootstrap.Prepared.Intent.Scope.Executable, bootstrap.SpoolRoot, grant.CommandID)
			}
			return invocationForGrant(ctx, grant)
		},
		EnsureCoordinator: func(ctx context.Context) error {
			hello := contract.HostHello{LaunchID: bootstrap.Prepared.Intent.Scope.LaunchID, OriginContextID: bootstrap.Prepared.Intent.Scope.OriginalOriginContextID, OriginPID: bootstrap.Prepared.Intent.Scope.OriginalOriginPID, OriginBirth: bootstrap.Prepared.Intent.Scope.OriginalOriginBirth, HostGeneration: bootstrap.Prepared.Intent.Scope.HostGeneration, PID: bootstrap.Prepared.Intent.FirstHost.PID, Birth: bootstrap.Prepared.Intent.FirstHost.Birth, Executable: bootstrap.Prepared.Intent.Scope.Executable}
			if err := coordinator.CheckRecoveryAllowed(bootstrap.StateDir, hello); err != nil {
				return err
			}
			_, err := ensureServerMode(ctx, bootstrap.StateDir, true)
			return err
		},
	})
}

// runInitialOriginalOwner consumes the first ordinary HostHello session only
// when the Coordinator persisted supervised-v1 for this launch.  A legacy
// response is deliberately left to RunSource, preserving the established CLI
// behavior and tests.
func runInitialOriginalOwner(ctx context.Context, bootstrap hostBootstrap) (bool, error) {
	statusBody, _ := json.Marshal(coordinator.HostStatusRequest{LaunchID: bootstrap.Hello.LaunchID, LaunchToken: bootstrap.Hello.LaunchToken})
	statusCtx, cancelStatus := context.WithTimeout(ctx, initialOwnerStatusTimeout)
	status, err := ipc.Call(statusCtx, bootstrap.SocketPath, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostStatus, RequestID: bootstrap.Hello.LaunchID + ":status", Payload: statusBody})
	cancelStatus()
	if err != nil {
		return false, err
	}
	if status.Kind != ipc.KindResponse {
		return false, errors.New("supervised_mode_unknown")
	}
	var presence contract.HostPresenceResponse
	if err = contract.DecodeOwnerRuntimeStrict(status.Payload, &presence); err != nil || presence.ExecutionOwnerMode == "" {
		return false, errors.New("supervised_mode_unknown")
	}
	if presence.ExecutionOwnerMode == "legacy" {
		return false, nil
	}
	if presence.ExecutionOwnerMode != "supervised-v1" {
		return false, errors.New("supervised_mode_unknown")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", bootstrap.SocketPath)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	if err = conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return false, err
	}
	hello, _ := json.Marshal(bootstrap.Hello)
	if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostHello, RequestID: bootstrap.Hello.LaunchID, Payload: hello}); err != nil {
		return false, err
	}
	pending, err := ipc.Read(conn)
	if err != nil {
		return false, err
	}
	if pending.Kind == ipc.KindHostReady {
		return true, errors.New("initial_owner_unexpected_ready")
	}
	if pending.Kind != ipc.KindOwnerHostPending {
		return false, errors.New("initial_owner_transport_rejected")
	}
	var ownerPending contract.OwnerHostPending
	if err = contract.DecodeOwnerRuntimeStrict(pending.Payload, &ownerPending); err != nil || pending.RequestID != bootstrap.Hello.LaunchID || pending.Epoch != ownerPending.CoordinatorEpoch {
		return true, errors.New("initial_owner_pending_rejected")
	}
	bootstrapID, err := randomValue("owner-bootstrap")
	if err != nil {
		return true, err
	}
	bootstrapToken, err := randomValue("owner-bootstrap-token")
	if err != nil {
		return true, err
	}
	tokenHash, err := contract.OriginalOwnerBootstrapSHA256(bootstrapToken)
	if err != nil {
		return true, err
	}
	prepareID, err := randomValue("owner-prepare")
	if err != nil {
		return true, err
	}
	startupDeadline := time.Now().Add(25 * time.Second)
	prepare, _ := json.Marshal(contract.OwnerPrepareRequest{Version: 1, BootstrapID: bootstrapID, BootstrapTokenSHA256: tokenHash, StartupDeadlineUnixMS: startupDeadline.UnixMilli()})
	if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerPrepare, RequestID: prepareID, Epoch: pending.Epoch, Payload: prepare}); err != nil {
		return true, err
	}
	preparedFrame, err := ipc.Read(conn)
	if err != nil {
		return true, err
	}
	var prepared contract.OwnerPrepared
	if preparedFrame.Kind != ipc.KindResponse || preparedFrame.RequestID != prepareID || preparedFrame.Epoch != pending.Epoch || contract.DecodeOwnerRuntimeStrict(preparedFrame.Payload, &prepared) != nil {
		return true, errors.New("initial_owner_prepare_rejected")
	}
	runtimeBootstrap := contract.OwnerRuntimeBootstrap{Version: 1, StateDir: filepath.Dir(bootstrap.SocketPath), SpoolRoot: bootstrap.SpoolRoot, Prepared: prepared, BootstrapToken: bootstrapToken}
	if err = runtimeBootstrap.Validate(); err != nil {
		return true, err
	}
	parentControl, childControl, err := ipc.NewOwnerControlPair()
	if err != nil {
		return true, err
	}
	defer parentControl.Close()
	defer childControl.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		return true, err
	}
	defer reader.Close()
	defer writer.Close()
	raw, _ := json.Marshal(runtimeBootstrap)
	child := exec.Command(bootstrap.Hello.Executable, "original-owner", "--bootstrap-fd", "3", "--control-fd", "4")
	child.ExtraFiles = []*os.File{reader, childControl}
	child.Env = sourceEnvironment()
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = child.Start(); err != nil {
		return true, err
	}
	owned := &initialOwnerChild{command: child, done: make(chan error, 1)}
	retire := func(cause error) error {
		return errors.Join(cause, owned.retire(initialOwnerDeadline(ctx, startupDeadline)))
	}
	_ = reader.Close()
	_ = childControl.Close()
	childIdentity := contract.OriginalOwnerTransport{PID: child.Process.Pid}
	childIdentity.Birth, err = process.BirthContext(ctx, childIdentity.PID)
	if err != nil || childIdentity.Validate() != nil {
		_ = writer.Close()
		// Birth observation has completed.  The original handle now owns the
		// sole Wait even though its tuple is unknown and must never be rebuilt.
		owned.startWait()
		return true, retire(errors.New("initial_owner_birth_unknown"))
	}
	owned.startWait()
	deadline := initialOwnerDeadline(ctx, startupDeadline)
	if err = writer.SetWriteDeadline(deadline); err != nil {
		return true, retire(err)
	}
	if _, err = writer.Write(raw); err != nil {
		return true, retire(err)
	}
	if err = writer.Close(); err != nil {
		return true, retire(err)
	}
	if err = parentControl.SetReadDeadline(deadline); err != nil {
		return true, retire(err)
	}
	boundFrame, err := ipc.Read(parentControl)
	if err != nil {
		return true, retire(err)
	}
	bound, err := host.VerifyInitialOwnerBound(runtimeBootstrap, childIdentity, boundFrame)
	if err != nil {
		return true, retire(err)
	}
	_ = parentControl.SetReadDeadline(time.Time{})
	// The verified Bound reply is the only authority for this public key.
	transport := contract.OwnerTransportBootstrap{Version: 1, StateDir: runtimeBootstrap.StateDir, Intent: prepared.Intent, Owner: bound.Binding.Owner}
	transport.CoordinatorPublicKey = prepared.CoordinatorPublicKey
	if err = host.RunInitialOwnerTransport(ctx, host.InitialOwnerTransportConfig{Bootstrap: transport, Control: parentControl, Connection: conn, Pending: pending, InitialToken: bootstrap.Hello.LaunchToken}); err != nil && !errors.Is(err, context.Canceled) {
		return true, retire(err)
	}
	select {
	case err = <-owned.done:
		return true, err
	case <-ctx.Done():
		return true, retire(ctx.Err())
	}
}

// initialOwnerChild retains the exact *exec.Cmd that observed Start.  It never
// reconstructs a PID and it never calls Wait before the caller has completed
// its one birth observation.
type initialOwnerChild struct {
	command *exec.Cmd
	done    chan error
	once    sync.Once
}

func (c *initialOwnerChild) startWait() {
	c.once.Do(func() { go func() { c.done <- c.command.Wait() }() })
}

func (c *initialOwnerChild) retire(deadline time.Time) error {
	if c == nil || c.command == nil || c.command.Process == nil {
		return errors.New("initial_owner_wait_missing")
	}
	c.startWait()
	_ = c.command.Process.Signal(syscall.SIGTERM)
	if err := waitInitialOwner(c.done, deadline); err == nil {
		return nil
	}
	_ = c.command.Process.Kill()
	// Keep the original waiter alive through a short post-KILL confirmation;
	// a caller deadline cannot turn a known child into an unowned PID.
	return waitInitialOwner(c.done, time.Now().Add(time.Second))
}

func waitInitialOwner(done <-chan error, deadline time.Time) error {
	if deadline.Before(time.Now()) {
		return context.DeadlineExceeded
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return context.DeadlineExceeded
	}
}

func initialOwnerDeadline(ctx context.Context, startup time.Time) time.Time {
	deadline := startup
	if caller, ok := ctx.Deadline(); ok && caller.Before(deadline) {
		deadline = caller
	}
	return deadline
}
