package host

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

var errOwnerTransportProtocol = errors.New("owner_transport_protocol_rejected")

// RunOwnerTransport owns only transport sockets and its exchanged credential.
// There is deliberately no Host, provider, spool writer or Ensure callback here.
func RunOwnerTransport(ctx context.Context, bootstrap contract.OwnerTransportBootstrap, control *net.UnixConn) error {
	if control == nil {
		return errOwnerTransportProtocol
	}
	defer control.Close()
	validate, cancel := context.WithTimeout(ctx, 5*time.Second)
	identity, err := validateOwnerTransportProcess(validate, bootstrap)
	cancel()
	if err != nil {
		return err
	}
	return runOwnerTransportLoop(ctx, bootstrap, identity, control)
}

func validateOwnerTransportProcess(ctx context.Context, b contract.OwnerTransportBootstrap) (contract.OriginalOwnerTransport, error) {
	if b.Validate() != nil {
		return contract.OriginalOwnerTransport{}, errOwnerTransportProtocol
	}
	return ownerProcessIdentity(ctx, b.StateDir, b.Intent.Scope, contract.OriginalOwnerTransport{PID: b.Owner.PID, Birth: b.Owner.Birth})
}

// Initial owners and replacement transports both inherit authority from an
// exact live parent. Their signed protocol roles remain separate.
func ownerProcessIdentity(ctx context.Context, stateDir string, scope contract.OriginalOwnerScope, parent contract.OriginalOwnerTransport) (contract.OriginalOwnerTransport, error) {
	var zero contract.OriginalOwnerTransport
	if parent.PID != os.Getppid() {
		return zero, errOwnerTransportProtocol
	}
	for _, p := range []contract.OriginalOwnerTransport{{PID: parent.PID, Birth: parent.Birth}, {PID: scope.OriginalOriginPID, Birth: scope.OriginalOriginBirth}} {
		birth, err := process.BirthContext(ctx, p.PID)
		if err != nil || birth != p.Birth {
			return zero, errOwnerTransportProtocol
		}
	}
	state, err := filepath.EvalSymlinks(stateDir)
	if err != nil || state != stateDir {
		return zero, errOwnerTransportProtocol
	}
	info, err := os.Lstat(state)
	stat, owned := infoSyscall(info)
	if err != nil || !owned || !info.IsDir() || info.Mode().Perm() != 0700 || stat.Uid != uint32(os.Geteuid()) {
		return zero, errOwnerTransportProtocol
	}
	executable, err := os.Executable()
	if err != nil {
		return zero, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil || executable != scope.Executable {
		return zero, errOwnerTransportProtocol
	}
	digest, err := process.ExecutableDigest(ctx, executable)
	if err != nil || digest != scope.ExecutableSHA256 {
		return zero, errOwnerTransportProtocol
	}
	birth, err := process.BirthContext(ctx, os.Getpid())
	if err != nil {
		return zero, err
	}
	identity := contract.OriginalOwnerTransport{PID: os.Getpid(), Birth: birth}
	if identity.Validate() != nil {
		return zero, errOwnerTransportProtocol
	}
	return identity, nil
}

type ownerTransportFrame struct {
	message ipc.Envelope
	err     error
}

func readOwnerTransportFrames(ctx context.Context, conn net.Conn) <-chan ownerTransportFrame {
	frames := make(chan ownerTransportFrame, 1)
	go func() {
		defer close(frames)
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()
		for {
			message, err := ipc.Read(conn)
			select {
			case frames <- ownerTransportFrame{message, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return frames
}

func writeOwnerTransportFrame(ctx context.Context, conn net.Conn, message ipc.Envelope) error {
	deadline := time.Now().Add(5 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	return ipc.Write(conn, message)
}

func runOwnerTransportLoop(parent context.Context, bootstrap contract.OwnerTransportBootstrap, identity contract.OriginalOwnerTransport, control *net.UnixConn) error {
	return runOwnerTransportLoopWithInitial(parent, bootstrap, identity, control, nil)
}

func runOwnerTransportLoopWithInitial(parent context.Context, bootstrap contract.OwnerTransportBootstrap, identity contract.OriginalOwnerTransport, control *net.UnixConn, initial *ownerTransportInitialSession) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer control.Close()
	controlFrames := readOwnerTransportFrames(ctx, control)
	public, _ := hex.DecodeString(bootstrap.CoordinatorPublicKey)
	key := ed25519.PublicKey(public)
	scope, _ := contract.OriginalOwnerScopeSHA256(bootstrap.Intent.Scope)
	owner, _ := contract.OriginalOwnerIdentitySHA256(bootstrap.Owner)
	minimumEpoch := bootstrap.Intent.CoordinatorEpoch
	var connection net.Conn
	var connectionFrames <-chan ownerTransportFrame
	var stopConnection context.CancelFunc
	closeConnection := func() {
		if stopConnection != nil {
			stopConnection()
			stopConnection = nil
		}
		if connection != nil {
			_ = connection.Close()
			connection = nil
		}
		connectionFrames = nil
	}
	defer closeConnection()
	token := "" // stays in this transport; never written to inherited control
	if initial != nil {
		if err := initial.validate(bootstrap, identity); err != nil {
			if initial.connection != nil {
				_ = initial.connection.Close()
			}
			return err
		}
		connection, token = initial.connection, initial.token
		if err := connection.SetDeadline(time.Time{}); err != nil {
			return err
		}
		connectionContext, cancelConnection := context.WithCancel(ctx)
		stopConnection = cancelConnection
		connectionFrames = readOwnerTransportFrames(connectionContext, connection)
	}
	notifyOffline := func(id string) error {
		raw, _ := json.Marshal(contract.OwnerTransportControl{Version: 1, Action: "offline"})
		return writeOwnerTransportFrame(ctx, control, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerTransportControl, RequestID: id, Payload: raw})
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case read := <-controlFrames:
			if read.err != nil {
				if errors.Is(read.err, io.EOF) {
					return nil
				}
				return read.err
			}
			frame := read.message
			request, err := contract.DecodeOwnerTransportControl(frame.Payload)
			if err != nil || frame.Kind != ipc.KindOwnerTransportControl || frame.Epoch != 0 || request.Action != "connect" {
				return errOwnerTransportProtocol
			}
			if request.Ticket != nil {
				ticket := *request.Ticket
				if ticket.OwnerID != bootstrap.Intent.Scope.OwnerID || ticket.ScopeSHA256 != scope || ticket.OwnerIdentitySHA256 != owner || ticket.Pending.NewHost != identity || ticket.CoordinatorEpoch < minimumEpoch || contract.VerifyOwnerTransportTicket(ticket, key, ticket.CoordinatorEpoch, time.Now().UnixMilli()) != nil {
					return errOwnerTransportProtocol
				}
				minimumEpoch = ticket.CoordinatorEpoch
			} else if token == "" {
				return errOwnerTransportProtocol
			}
			closeConnection()
			operation, stop := context.WithTimeout(ctx, 5*time.Second)
			conn, pending, err := connectOwnerTransport(operation, bootstrap, identity, request.Ticket, &token, minimumEpoch)
			stop()
			if err != nil {
				if errors.Is(err, errOwnerTransportProtocol) {
					return err
				}
				if err = notifyOffline(frame.RequestID); err != nil {
					return err
				}
				continue // no timer/retry/Ensure: only the owner may request another dial
			}
			connection = conn
			minimumEpoch = pending.Epoch
			connectionContext, cancelConnection := context.WithCancel(ctx)
			stopConnection = cancelConnection
			connectionFrames = readOwnerTransportFrames(connectionContext, connection)
			if err = writeOwnerTransportFrame(ctx, control, pending); err != nil {
				return err
			}
		case read := <-connectionFrames:
			if read.err != nil {
				closeConnection()
				var network net.Error
				if !errors.Is(read.err, io.EOF) && !errors.As(read.err, &network) {
					return errOwnerTransportProtocol
				}
				if err := notifyOffline("transport-offline"); err != nil {
					return err
				}
				continue
			}
			frame := read.message
			command, err := contract.DecodeOwnerCommand(frame.Payload)
			if err != nil || frame.Kind != ipc.KindOwnerCommand || frame.RequestID != command.RequestID || frame.Epoch != minimumEpoch || frame.Epoch != command.Permit.CoordinatorEpoch || command.Permit.OwnerID != bootstrap.Intent.Scope.OwnerID || command.Permit.ScopeSHA256 != scope || command.Permit.OwnerIdentitySHA256 != owner || command.Permit.NewHost != identity || contract.VerifyOwnerCommand(command, key, command.Permit) != nil {
				return errOwnerTransportProtocol
			}
			// The owner independently checks this full signed command against its
			// exact current permit. An unsigned pending notification never admits work.
			if err = writeOwnerTransportFrame(ctx, control, frame); err != nil {
				return err
			}
		}
	}
}

func connectOwnerTransport(ctx context.Context, b contract.OwnerTransportBootstrap, identity contract.OriginalOwnerTransport, ticket *contract.OwnerTransportTicket, token *string, minimumEpoch uint64) (net.Conn, ipc.Envelope, error) {
	var zero ipc.Envelope
	if ticket != nil {
		raw, _ := json.Marshal(ticket)
		reply, err := ipc.Call(ctx, filepath.Join(b.StateDir, "owner.sock"), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerTransportExchange, RequestID: ticket.Pending.RequestID, Epoch: ticket.CoordinatorEpoch, Payload: raw})
		if err != nil {
			return nil, zero, err
		}
		if reply.Kind == ipc.KindError {
			return nil, zero, errors.New("owner_transport_exchange_refused")
		}
		var credential contract.OwnerTransportCredential
		if reply.Kind != ipc.KindResponse || reply.RequestID != ticket.Pending.RequestID || reply.Epoch != ticket.CoordinatorEpoch || contract.DecodeOwnerRuntimeStrict(reply.Payload, &credential) != nil || credential.LaunchToken == "" || len(credential.LaunchToken) > 4096 || strings.ContainsRune(credential.LaunchToken, 0) {
			return nil, zero, errOwnerTransportProtocol
		}
		*token = credential.LaunchToken
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(b.StateDir, "coordinator.sock"))
	if err != nil {
		return nil, zero, err
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = conn.Close()
		}
	}()
	// Cancellation may race the successful handshake. stopClose must succeed
	// before handing off; otherwise this connection is not a usable session.
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
	if err = setConnDeadline(conn, ctx); err != nil {
		return nil, zero, err
	}
	s := b.Intent.Scope
	hello := contract.HostHello{LaunchID: s.LaunchID, LaunchToken: *token, OriginContextID: s.OriginalOriginContextID, OriginPID: s.OriginalOriginPID, OriginBirth: s.OriginalOriginBirth, HostGeneration: s.HostGeneration, PID: identity.PID, Birth: identity.Birth, Executable: s.Executable}
	raw, _ := json.Marshal(hello)
	if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostHello, RequestID: s.LaunchID, Payload: raw}); err != nil {
		return nil, zero, err
	}
	frame, err := ipc.Read(conn)
	if err != nil {
		return nil, zero, err
	}
	if frame.Kind == ipc.KindError || frame.Kind == ipc.KindHostDisposition {
		return nil, zero, errors.New("owner_transport_hello_refused")
	}
	pending, err := contract.DecodeOwnerHostPending(frame.Payload)
	if err != nil || frame.Kind != ipc.KindOwnerHostPending || frame.RequestID != s.LaunchID || frame.Epoch != pending.CoordinatorEpoch || frame.Epoch < minimumEpoch || pending.HostID != s.ProducerID || pending.OwnerID != s.OwnerID || pending.CoordinatorPublicKey != b.CoordinatorPublicKey {
		return nil, zero, errOwnerTransportProtocol
	}
	if !stopClose() || ctx.Err() != nil {
		return nil, zero, ctx.Err()
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		return nil, zero, err
	}
	accepted = true
	return conn, frame, nil
}
