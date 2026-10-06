package host

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// This object belongs to the original owner, never a PID-based reattachment.
// The private reservation copy ties its one real Start/Wait to the recovery slot.
type ownerTransportChild struct {
	command       *exec.Cmd
	control       *net.UnixConn
	reservation   ownerRecoveryPending
	recoveryScope string
	identity      contract.OriginalOwnerTransport
	done          chan struct{}
	waitError     error
}

func startOwnerTransport(ctx context.Context, bootstrap contract.OwnerTransportBootstrap, pending ownerRecoveryPending, environment []string) (*ownerTransportChild, error) {
	return startOwnerTransportWith(ctx, bootstrap, pending, environment, []string{"owner-transport", "--bootstrap-fd", "3", "--control-fd", "4"}, process.BirthContext)
}

// args/birth are private launch seams; the runtime entry point above fixes both.
func startOwnerTransportWith(parent context.Context, bootstrap contract.OwnerTransportBootstrap, pending ownerRecoveryPending, environment, args []string, birth func(context.Context, int) (string, error)) (*ownerTransportChild, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if bootstrap.Validate() != nil || bootstrap.Owner.PID != os.Getpid() || pending.Phase != "starting" || pending.NewHost != nil || pending.StartSlot < 1 || pending.StartSlot > 5 || pending.RequestID == "" || len(pending.RequestID) > 256 || !utf8.ValidString(pending.RequestID) || strings.ContainsRune(pending.RequestID, 0) || pending.ExpectedControlRevision == 0 || pending.ExpectedControlRevision >= math.MaxInt64 || pending.OldHost.Validate() != nil || birth == nil {
		return nil, ownerRecoveryRejected()
	}
	scope, err := originalOwnerRecoveryScope(bootstrap.Intent.Scope, bootstrap.Owner)
	if err != nil {
		return nil, err
	}
	selfBirth, err := process.BirthContext(ctx, os.Getpid())
	if err != nil || selfBirth != bootstrap.Owner.Birth {
		return nil, ownerRecoveryRejected()
	}
	state, err := filepath.EvalSymlinks(bootstrap.StateDir)
	if err != nil || state != bootstrap.StateDir {
		return nil, ownerRecoveryRejected()
	}
	info, err := os.Lstat(state)
	stat, owned := infoSyscall(info)
	if err != nil || !owned || !info.IsDir() || info.Mode().Perm() != 0700 || stat.Uid != uint32(os.Geteuid()) {
		return nil, ownerRecoveryRejected()
	}
	executable := bootstrap.Intent.Scope.Executable
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil || resolved != executable {
		return nil, ownerRecoveryRejected()
	}
	digest, err := process.ExecutableDigest(ctx, executable)
	if err != nil || digest != bootstrap.Intent.Scope.ExecutableSHA256 {
		return nil, ownerRecoveryRejected()
	}
	body, err := json.Marshal(bootstrap)
	if err != nil || len(body) > contract.OriginalOwnerEnvelopeLimit {
		return nil, ownerRecoveryRejected()
	}
	control, childControl, err := ipc.NewOwnerControlPair()
	if err != nil {
		return nil, err
	}
	defer childControl.Close()
	started := false
	defer func() {
		if !started {
			_ = control.Close()
		}
	}()
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	defer writer.Close()
	stop := context.AfterFunc(ctx, func() { _ = writer.Close() })
	defer stop()
	command := exec.Command(executable, args...) // owner, not launch context, owns lifetime
	command.ExtraFiles = []*os.File{reader, childControl}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Env = ownerTransportEnvironment(environment)
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = command.Start(); err != nil {
		return nil, err
	}
	started = true
	child := &ownerTransportChild{command: command, control: control, reservation: pending, recoveryScope: scope, identity: contract.OriginalOwnerTransport{PID: command.Process.Pid}, done: make(chan struct{})}
	go func() {
		child.waitError = command.Wait()
		close(child.done)
	}()
	_ = reader.Close()
	_ = childControl.Close()
	deadline, _ := ctx.Deadline()
	if err = writer.SetWriteDeadline(deadline); err != nil {
		return child, err
	}
	if _, err = writer.Write(body); err != nil {
		return child, err
	}
	if err = writer.Close(); err != nil {
		return child, err
	}
	child.identity.Birth, err = birth(ctx, child.identity.PID)
	if err != nil || child.identity.Validate() != nil {
		child.identity.Birth = ""
		return child, errors.New("owner_transport_birth_unknown")
	}
	return child, nil
}

func (c *ownerTransportChild) wait(ctx context.Context) error {
	select {
	case <-c.done:
		return c.waitError
	default:
	}
	select {
	case <-c.done:
		return c.waitError
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *ownerTransportChild) waited() bool {
	select {
	case <-c.done:
		return c.command.ProcessState != nil
	default:
		return false
	}
}

func ownerTransportEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "ORCHESTRATOR_") && key != "ORCHESTRATOR_ENABLE_TEST_FAKE" {
			continue
		}
		result = append(result, entry)
	}
	return result
}
