package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// Private native-fixture seam. Production leaves it nil. It runs in FirstHost
// after the signed descriptor-close ACK and before writing any source payload.
var ownerLaunchBeforePayload func(context.Context, contract.OwnerRuntimeBootstrap) error

func installSupervisedOwnerEnvironment(ctx context.Context, b contract.OwnerRuntimeBootstrap) error {
	if b.Version != 2 {
		return nil
	}
	if b.Validate() != nil || b.LaunchReceipt.Child.PID != os.Getpid() || b.LaunchReceipt.Supervisor.PID != os.Getppid() {
		return codeError("owner_launch_witness_rejected")
	}
	// Check the signed physical launch with the fixed non-auth environment
	// before installing any source value. Parent/source authentication remains
	// independently checked by the runtime after installation.
	self, err := process.BirthContext(ctx, os.Getpid())
	if err != nil || self != b.LaunchReceipt.Child.Birth {
		return codeError("owner_launch_witness_rejected")
	}
	parent, err := process.BirthContext(ctx, os.Getppid())
	if err != nil || parent != b.LaunchReceipt.Supervisor.Birth {
		return codeError("owner_launch_witness_rejected")
	}
	path, err := resolvedExecutable()
	if err != nil || path != b.Prepared.Intent.Scope.Executable {
		return codeError("owner_launch_witness_rejected")
	}
	digest, err := process.ExecutableDigest(ctx, path)
	if err != nil || digest != b.Prepared.Intent.Scope.ExecutableSHA256 {
		return codeError("owner_launch_witness_rejected")
	}
	os.Clearenv()
	for _, entry := range b.Environment {
		key, value, _ := strings.Cut(entry, "=")
		if err = os.Setenv(key, value); err != nil {
			return codeError("owner_launch_environment_rejected")
		}
	}
	return nil
}

func runCoordinatorOriginalOwner(ctx context.Context, source hostBootstrap, conn net.Conn, pending ipc.Envelope, prepared contract.OwnerPrepared, token string) error {
	a := prepared.LaunchAttempt
	if a == nil || a.Validate() != nil || a.EffectiveIntent != prepared.Intent {
		return codeError("owner_launch_prepare_rejected")
	}
	key, err := hex.DecodeString(prepared.CoordinatorPublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return codeError("owner_launch_prepare_rejected")
	}
	parentControl, childControl, err := ipc.NewOwnerControlPair()
	if err != nil {
		return err
	}
	defer parentControl.Close()
	defer childControl.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer reader.Close()
	defer writer.Close()
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return codeError("owner_launch_transport_rejected")
	}
	request := coordinator.OwnerLaunchRequest{OwnerID: prepared.Intent.Scope.OwnerID, AttemptSHA256: a.AttemptSHA256}
	body, _ := json.Marshal(request)
	id := prepared.Intent.RequestID + ":launch"
	deadline := initialOwnerDeadline(ctx, time.UnixMilli(prepared.Intent.StartupDeadlineUnixMS))
	if err = conn.SetDeadline(deadline); err != nil {
		return err
	}
	if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerLaunch, RequestID: id, Epoch: pending.Epoch, Payload: body}); err != nil {
		return err
	}
	if err = ipc.SendOwnerLaunchFDs(ctx, unixConn, reader, childControl); err != nil {
		return err
	}
	// Drop source copies of the child endpoints as well. No environment has
	// entered either channel, and no actor can mistake a queued close for proof.
	if err = errors.Join(reader.Close(), childControl.Close()); err != nil {
		return err
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return err
	}
	frame, err := ipc.Read(conn)
	if err != nil {
		return err
	}
	if frame.Kind == ipc.KindError && frame.RequestID == id && frame.Epoch == pending.Epoch {
		var remote coordinator.ErrorResponse
		if contract.DecodeOwnerRuntimeStrict(frame.Payload, &remote) == nil && remote.Error != "" {
			return remoteError(remote.Error)
		}
	}
	var receipt contract.OriginalOwnerLaunchReceipt
	if frame.Kind != ipc.KindResponse || frame.RequestID != id || frame.Epoch != pending.Epoch || contract.DecodeOwnerRuntimeStrict(frame.Payload, &receipt) != nil || contract.VerifyOriginalOwnerLaunchReceipt(receipt, ed25519.PublicKey(key), *a) != nil || receipt.Phase != contract.OriginalOwnerLaunchStartedWaitOwned || receipt.Child == nil || !receipt.HandoffClosed {
		return codeError("owner_launch_receipt_rejected")
	}
	runtimeBootstrap := contract.OwnerRuntimeBootstrap{Version: 2, StateDir: sourceStateDir(source), SpoolRoot: source.SpoolRoot, Prepared: prepared, BootstrapToken: token, LaunchReceipt: &receipt, Environment: sourceEnvironment()}
	if err = runtimeBootstrap.Validate(); err != nil {
		return err
	}
	if ownerLaunchBeforePayload != nil {
		if err = ownerLaunchBeforePayload(ctx, runtimeBootstrap); err != nil {
			return err
		}
	}
	body, err = json.Marshal(runtimeBootstrap)
	if err != nil {
		return err
	}
	if err = writer.SetWriteDeadline(deadline); err != nil {
		return err
	}
	if _, err = writer.Write(body); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	if err = parentControl.SetReadDeadline(deadline); err != nil {
		return err
	}
	boundFrame, err := ipc.Read(parentControl)
	if err != nil {
		return err
	}
	bound, err := host.VerifyInitialOwnerBound(runtimeBootstrap, *receipt.Child, boundFrame)
	if err != nil {
		return err
	}
	if err = parentControl.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	transport := contract.OwnerTransportBootstrap{Version: 1, StateDir: runtimeBootstrap.StateDir, Intent: prepared.Intent, Owner: bound.Binding.Owner, CoordinatorPublicKey: prepared.CoordinatorPublicKey}
	err = host.RunInitialOwnerTransport(ctx, host.InitialOwnerTransportConfig{Bootstrap: transport, Control: parentControl, Connection: conn, Pending: pending, InitialToken: source.Hello.LaunchToken})
	if err != nil && !host.IsInitialOwnerControlReset(err) {
		return err
	}
	return waitCoordinatorOwnerReceipt(ctx, source, request, *a, ed25519.PublicKey(key))
}

func sourceStateDir(source hostBootstrap) string {
	// SocketPath has already passed private bootstrap validation.
	return filepath.Dir(source.SocketPath)
}

func waitCoordinatorOwnerReceipt(parent context.Context, source hostBootstrap, launch coordinator.OwnerLaunchRequest, attempt contract.OriginalOwnerLaunchAttempt, key ed25519.PublicKey) error {
	ctx, cancel := context.WithTimeout(parent, 13*time.Second)
	defer cancel()
	body, _ := json.Marshal(coordinator.OwnerLaunchStatusRequest{OwnerLaunchRequest: launch, Hello: source.Hello})
	for {
		op, stop := context.WithTimeout(ctx, 2*time.Second)
		frame, err := ipc.Call(op, source.SocketPath, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerLaunchStatus, RequestID: launch.OwnerID + ":wait", Payload: body})
		stop()
		if err != nil {
			return errors.Join(codeError("owner_launch_wait_unknown"), err)
		}
		var receipt contract.OriginalOwnerLaunchReceipt
		if frame.Kind != ipc.KindResponse || contract.DecodeOwnerRuntimeStrict(frame.Payload, &receipt) != nil || contract.VerifyOriginalOwnerLaunchReceipt(receipt, key, attempt) != nil {
			return codeError("owner_launch_wait_unknown")
		}
		if receipt.Phase == contract.OriginalOwnerLaunchWaited {
			if receipt.ExitCode != nil && *receipt.ExitCode == 0 && receipt.ExitSignal == "" {
				return nil
			}
			return codeError("owner_launch_exit_failed")
		}
		if receipt.Phase != contract.OriginalOwnerLaunchStartedWaitOwned {
			return codeError("owner_launch_wait_unknown")
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
