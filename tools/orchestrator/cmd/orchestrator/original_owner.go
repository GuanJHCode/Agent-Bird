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
	"syscall"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitopsworker"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

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
	})
}

// runInitialOriginalOwner consumes the first ordinary HostHello session only
// when the Coordinator persisted supervised-v1 for this launch.  A legacy
// response is deliberately left to RunSource, preserving the established CLI
// behavior and tests.
func runInitialOriginalOwner(ctx context.Context, bootstrap hostBootstrap) (bool, error) {
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
		return false, nil
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
	prepare, _ := json.Marshal(contract.OwnerPrepareRequest{Version: 1, BootstrapID: bootstrapID, BootstrapTokenSHA256: tokenHash, StartupDeadlineUnixMS: time.Now().Add(25 * time.Second).UnixMilli()})
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
	_ = reader.Close()
	_ = childControl.Close()
	childIdentity := contract.OriginalOwnerTransport{PID: child.Process.Pid}
	childIdentity.Birth, err = process.BirthContext(ctx, childIdentity.PID)
	if err != nil || childIdentity.Validate() != nil {
		_ = writer.Close()
		return true, errors.New("initial_owner_birth_unknown")
	}
	waited := make(chan error, 1)
	go func() { waited <- child.Wait() }()
	if _, err = writer.Write(raw); err != nil {
		return true, err
	}
	if err = writer.Close(); err != nil {
		return true, err
	}
	if err = parentControl.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
		return true, err
	}
	boundFrame, err := ipc.Read(parentControl)
	if err != nil {
		return true, err
	}
	if _, err = host.VerifyInitialOwnerBound(runtimeBootstrap, childIdentity, boundFrame); err != nil {
		return true, err
	}
	_ = parentControl.SetReadDeadline(time.Time{})
	transport := contract.OwnerTransportBootstrap{Version: 1, StateDir: runtimeBootstrap.StateDir, Intent: prepared.Intent, Owner: contract.OriginalOwnerIdentity{PID: childIdentity.PID, Birth: childIdentity.Birth}}
	// The verified Bound reply is the only authority for this public key.
	var wire struct {
		Binding contract.OriginalOwnerBinding `json:"binding"`
	}
	_ = contract.DecodeOwnerRuntimeStrict(boundFrame.Payload, &wire)
	transport.Owner = wire.Binding.Owner
	transport.CoordinatorPublicKey = prepared.CoordinatorPublicKey
	if err = host.RunInitialOwnerTransport(ctx, host.InitialOwnerTransportConfig{Bootstrap: transport, Control: parentControl, Connection: conn, Pending: pending, InitialToken: bootstrap.Hello.LaunchToken}); err != nil && !errors.Is(err, context.Canceled) {
		return true, err
	}
	select {
	case err = <-waited:
		return true, err
	case <-ctx.Done():
		return true, ctx.Err()
	}
}
