package coordinator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type OwnerLaunchRequest struct {
	OwnerID       string `json:"owner_id"`
	AttemptSHA256 string `json:"attempt_sha256"`
}

type OwnerLaunchStatusRequest struct {
	OwnerLaunchRequest
	Hello contract.HostHello `json:"hello"`
}

// Only this process that called Start owns the Cmd and its sole Wait. Entries
// are bounded by the Host admission limit; terminal history lives in Store.
type originalOwnerLaunchChild struct {
	command   *exec.Cmd
	ready     chan struct{}
	done      chan struct{}
	receipt   contract.OriginalOwnerLaunchReceipt
	identity  contract.OriginalOwnerTransport
	waitErr   error
	persisted bool // published by closing done
}

// Caller holds registration serialization, including during Start. The
// instance is never reconstructed from a database row after Coordinator death.
func (s *Server) originalOwnerLaunchSupervisor(ctx context.Context, executableSHA string) (contract.OriginalOwnerLaunchSupervisor, error) {
	if prior := s.currentLaunchSupervisor(); prior != nil {
		if prior.ExecutableSHA256 != executableSHA {
			return contract.OriginalOwnerLaunchSupervisor{}, ownerRejected()
		}
		return *prior, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return contract.OriginalOwnerLaunchSupervisor{}, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return contract.OriginalOwnerLaunchSupervisor{}, err
	}
	digest, err := process.ExecutableDigest(ctx, executable)
	if err != nil || digest != executableSHA {
		return contract.OriginalOwnerLaunchSupervisor{}, ownerRejected()
	}
	birth, err := process.BirthContext(ctx, os.Getpid())
	if err != nil {
		return contract.OriginalOwnerLaunchSupervisor{}, err
	}
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return contract.OriginalOwnerLaunchSupervisor{}, err
	}
	instance := contract.OriginalOwnerLaunchSupervisor{Epoch: s.epoch, PID: os.Getpid(), Birth: birth, Nonce: hex.EncodeToString(nonce[:]), ExecutableSHA256: digest}
	if instance.Validate() != nil {
		return instance, ownerRejected()
	}
	s.mu.Lock()
	s.launchSupervisor = &instance
	s.mu.Unlock()
	s.launchChildren = make(map[string]*originalOwnerLaunchChild)
	return instance, nil
}

func (s *Server) currentLaunchSupervisor() *contract.OriginalOwnerLaunchSupervisor {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.launchSupervisor == nil {
		return nil
	}
	copy := *s.launchSupervisor
	return &copy
}

// Source authentication values are absent. In particular HOME, provider
// credentials and source PATH are not inherited from the Coordinator.
func originalOwnerLaunchEnvironment() []string {
	return []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
}

func (s *Server) launchOriginalOwner(ctx context.Context, session *hostSession, request ipc.Envelope, descriptors *ipc.OwnerLaunchFDs) (contract.OriginalOwnerLaunchReceipt, error) {
	var zero contract.OriginalOwnerLaunchReceipt
	var launch OwnerLaunchRequest
	if contract.DecodeOwnerRuntimeStrict(request.Payload, &launch) != nil || request.Epoch != s.epoch || s.closed.Load() || s.terminating.Load() {
		return zero, ownerRejected()
	}
	s.mu.Lock()
	current := s.ownerTransports[session.id] == session
	s.mu.Unlock()
	if !current || s.authorizeHostHello(ctx, session.conn, session.hello) != nil {
		return zero, ownerRejected()
	}
	state, err := s.db.InspectOriginalOwnerLaunch(ctx, launch.OwnerID)
	if err != nil {
		return zero, err
	}
	a := state.LaunchAttempt
	if a == nil || a.AttemptSHA256 != launch.AttemptSHA256 || state.Intent.FirstHost != (contract.OriginalOwnerTransport{PID: session.hello.PID, Birth: session.hello.Birth}) || state.Phase != "intended" || !state.IsCurrent || state.Intent.StartupDeadlineUnixMS <= time.Now().UnixMilli() {
		return zero, ownerRejected()
	}
	instance, err := s.originalOwnerLaunchSupervisor(ctx, state.Intent.Scope.ExecutableSHA256)
	if err != nil || instance != a.Supervisor {
		return zero, ownerRejected()
	}
	if prior := s.launchChildren[launch.AttemptSHA256]; prior != nil {
		// A duplicate descriptor set never leads to another process. Once the
		// original terminal is observed the source must use explicit recovery.
		select {
		case <-prior.done:
			if !prior.persisted {
				return zero, store.CodeError("owner_launch_unknown")
			}
			return zero, store.CodeError("owner_launch_finished")
		default:
		}
		if prior.receipt.Phase != contract.OriginalOwnerLaunchStartedWaitOwned {
			return zero, store.CodeError("owner_launch_unknown")
		}
		return prior.receipt, nil
	}
	if descriptors.ValidateEmpty() != nil {
		return zero, ownerRejected()
	}
	path, err := filepath.EvalSymlinks(state.Intent.Scope.Executable)
	if err != nil || path != state.Intent.Scope.Executable {
		return zero, ownerRejected()
	}
	digest, err := process.ExecutableDigest(ctx, path)
	if err != nil || digest != instance.ExecutableSHA256 {
		return zero, ownerRejected()
	}
	starting, err := s.db.RecordOriginalOwnerLaunchStartIntent(ctx, launch.OwnerID, launch.AttemptSHA256, instance, s.ownerKeys.sign)
	if err != nil {
		return zero, err
	}
	if starting.Phase != contract.OriginalOwnerLaunchStarting {
		return zero, store.CodeError("owner_launch_unknown")
	}
	command := exec.Command(path, "original-owner", "--bootstrap-fd", "3", "--control-fd", "4")
	command.ExtraFiles = descriptors.Files()
	command.Env = originalOwnerLaunchEnvironment()
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	child := &originalOwnerLaunchChild{command: command, ready: make(chan struct{}), done: make(chan struct{})}
	s.launchChildren[launch.AttemptSHA256] = child
	if err = ctx.Err(); err == nil {
		err = descriptors.ValidateEmpty()
	}
	if err == nil {
		err = command.Start()
	}
	if err != nil {
		// No successful Start occurred in this exact instance. A failed
		// observation after Start must never enter this branch.
		_, recordErr := s.db.RecordOriginalOwnerLaunchNoStart(context.Background(), launch.OwnerID, launch.AttemptSHA256, instance, s.ownerKeys.sign)
		child.persisted = recordErr == nil
		close(child.ready)
		close(child.done)
		if child.persisted {
			delete(s.launchChildren, launch.AttemptSHA256)
		}
		return zero, errors.Join(err, recordErr)
	}
	child.identity = contract.OriginalOwnerTransport{PID: command.Process.Pid}
	defer close(child.ready)
	// Capture birth while the child is still unreaped. Close all Coordinator
	// source endpoints before any signed receipt authorizes writing payload.
	child.identity.Birth, err = process.BirthContext(ctx, child.identity.PID)
	closeErr := descriptors.Close()
	command.ExtraFiles = nil
	known := err == nil && child.identity.Validate() == nil && closeErr == nil
	go s.waitOriginalOwnerLaunch(launch, instance, child, known)
	if !known {
		return zero, errors.Join(store.CodeError("owner_launch_unknown"), err, closeErr)
	}
	child.receipt, err = s.db.RecordOriginalOwnerLaunchStarted(ctx, launch.OwnerID, launch.AttemptSHA256, instance, child.identity, true, s.ownerKeys.sign)
	if err != nil {
		return zero, err
	}
	return child.receipt, nil
}

func (s *Server) waitOriginalOwnerLaunch(launch OwnerLaunchRequest, instance contract.OriginalOwnerLaunchSupervisor, child *originalOwnerLaunchChild, known bool) {
	child.waitErr = child.command.Wait()
	<-child.ready
	defer func() {
		close(child.done)
		s.hostRegistrationMu.Lock()
		if child.persisted && s.launchChildren[launch.AttemptSHA256] == child {
			delete(s.launchChildren, launch.AttemptSHA256)
		}
		s.hostRegistrationMu.Unlock()
	}()
	if !known || child.command.ProcessState == nil || child.command.ProcessState.Pid() != child.identity.PID {
		return
	}
	// Retrying the exact durable started write is safe: it cannot call Start,
	// alter the child identity or change whether descriptor closure occurred.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if child.receipt.Phase != contract.OriginalOwnerLaunchStartedWaitOwned {
		if _, err := s.db.RecordOriginalOwnerLaunchStarted(ctx, launch.OwnerID, launch.AttemptSHA256, instance, child.identity, true, s.ownerKeys.sign); err != nil {
			return
		}
	}
	status, ok := child.command.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		return
	}
	var result store.OriginalOwnerLaunchExit
	if status.Signaled() {
		result.Signal = "SIG" + strconv.Itoa(int(status.Signal()))
	} else if status.Exited() {
		code := status.ExitStatus()
		result.Code = &code
	} else {
		return
	}
	_, err := s.db.RecordOriginalOwnerLaunchWait(ctx, launch.OwnerID, launch.AttemptSHA256, instance, child.identity, result, s.ownerKeys.sign)
	child.persisted = err == nil
}

func (s *Server) handleOwnerLaunchStatus(ctx context.Context, conn net.Conn, request ipc.Envelope) {
	var query OwnerLaunchStatusRequest
	var result any
	err := contract.DecodeOwnerRuntimeStrict(request.Payload, &query)
	if err == nil {
		err = s.authorizeHostHello(ctx, conn, query.Hello)
	}
	if err == nil {
		var state store.OriginalOwnerState
		state, err = s.db.InspectOriginalOwnerLaunch(ctx, query.OwnerID)
		if err == nil && (state.LaunchAttempt == nil || state.LaunchAttempt.AttemptSHA256 != query.AttemptSHA256 || state.Intent.FirstHost != (contract.OriginalOwnerTransport{PID: query.Hello.PID, Birth: query.Hello.Birth})) {
			err = ownerRejected()
		}
		if err == nil {
			result = state.LaunchReceipt
		}
	}
	if err != nil {
		_ = sendError(conn, request.RequestID, s.epoch, err)
		return
	}
	body, err := json.Marshal(result)
	if err != nil {
		_ = sendError(conn, request.RequestID, s.epoch, err)
		return
	}
	_ = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindResponse, RequestID: request.RequestID, Epoch: s.epoch, Payload: body})
}

// Close holds registration serialization and has stopped accepting new owner
// RPCs. Signal only retained original process objects. A bounded failure keeps
// UNKNOWN; it never becomes a PID-based cleanup or a synthetic Wait receipt.
func (s *Server) closeOriginalOwnerLaunches(ctx context.Context) error {
	for _, child := range s.launchChildren {
		select {
		case <-child.done:
			continue
		default:
		}
		if child.command != nil && child.command.Process != nil {
			_ = child.command.Process.Signal(syscall.SIGTERM)
		}
	}
	for _, child := range s.launchChildren {
		select {
		case <-child.done:
		case <-ctx.Done():
			return store.CodeError("owner_launch_wait_unknown")
		}
	}
	return nil
}
