package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type HostRecoveryRequest struct {
	TaskControlRequest
	OwnerCapability string `json:"owner_capability"`
}

func (s *Server) inspectHostRecovery(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var req HostRecoveryRequest
	if err := strictJSON(raw, &req); err != nil {
		return nil, err
	}
	owner, err := s.localOwner(ctx, req.OwnerCapability)
	if err != nil {
		return nil, err
	}
	s.hostRegistrationMu.Lock()
	defer s.hostRegistrationMu.Unlock()
	r, err := s.db.InspectHostRecovery(ctx, req.TaskID, req.ControllerThread, req.ControlToken, req.WorkRevision)
	if err != nil {
		return nil, err
	}
	if owner.ControllerThread != req.ControllerThread || owner.OriginPID != r.OriginPID || owner.OriginBirth != r.OriginBirth || owner.HostGeneration != r.HostGeneration {
		return nil, store.CodeError("owner_mismatch")
	}
	exited, err := hostIdentityExited(r.PID, r.Birth)
	if err != nil {
		return nil, err
	}
	if !exited {
		if r.Status != "ready" {
			return nil, store.CodeError("host_recovery_host_alive")
		}
		path, err := process.ExecutablePath(r.PID)
		if err != nil || path != r.Executable {
			return nil, store.CodeError("host_recovery_identity_unknown")
		}
	} else if r.Status == "ready" {
		return nil, store.CodeError("host_recovery_unavailable")
	}
	return json.Marshal(r)
}

// An observation failure is not proof of exit. A reused PID has a different
// birth and cannot be treated as the old Host or signalled by recovery.
func hostIdentityExited(pid int, birth string) (bool, error) {
	if pid <= 1 || birth == "" {
		return false, store.CodeError("host_recovery_identity_unknown")
	}
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true, nil
	} else if err != nil {
		return false, store.CodeError("host_recovery_identity_unknown")
	}
	current, err := process.Birth(pid)
	if err != nil {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return true, nil
		}
		return false, store.CodeError("host_recovery_identity_unknown")
	}
	return current != birth, nil
}

// Serialize admission so two valid peers cannot both replace a retired Host.
// The same registered peer may still reconnect; a different live peer may not.
func (s *Server) registerHost(ctx context.Context, hello contract.HostHello) (string, error) {
	s.hostRegistrationMu.Lock()
	defer s.hostRegistrationMu.Unlock()
	binding, err := s.db.HostBinding(ctx, hello.LaunchID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if err == nil && (binding.PID != hello.PID || binding.Birth != hello.Birth) {
		exited, err := hostIdentityExited(binding.PID, binding.Birth)
		if err != nil || !exited {
			return "", store.ErrHostRejected
		}
		if binding.Active > 0 {
			if err = s.db.AuthorizeHostRebind(ctx, hello.LaunchID, binding.PID, binding.Birth); err != nil {
				return "", store.ErrHostRejected
			}
		}
	}
	return s.db.RegisterHost(ctx, hello, s.epoch)
}
