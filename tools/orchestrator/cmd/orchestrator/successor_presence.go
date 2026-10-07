package main

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

func ensureSuccessorSourceHostPresence(parent context.Context, commandID string, inspect func(context.Context) (contract.HostPresenceResponse, error), start func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	started := false
	for {
		if ctx.Err() != nil {
			if parent.Err() != nil {
				return parent.Err()
			}
			return codeError("source_host_start_timeout")
		}
		presence, err := inspect(ctx)
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			continue
		}
		if presence.ExecutionOwnerMode != "supervised-v1" || (presence.OwnerPhase != "succession_pending" && presence.OwnerPhase != "intended" && presence.OwnerPhase != "bound") {
			return codeError("host_recovery_identity_unknown")
		}
		// An immutable old receipt is not authority for the current selector.
		if presence.SuccessorCommandID != commandID {
			return nil
		}
		switch presence.OwnerPhase {
		case "succession_pending":
			if presence.Presence != "old_host_exited" {
				return codeError("host_recovery_identity_unknown")
			}
			if !started {
				if err := start(ctx); err != nil {
					return err
				}
				started = true
			}
		case "intended", "bound":
			switch presence.Presence {
			case "old_host_exited":
				return codeError("owner_resume_pending_blocked")
			case "same_host_alive":
				if presence.Status == "ready" {
					return nil
				}
			default:
				return codeError("host_recovery_identity_unknown")
			}
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func ensureSuccessorSourceHostWith(ctx context.Context, state string, capability controlCapability, receipt coordinator.ResumeResponse, rpc func(context.Context, string, ipc.Kind, any) (ipc.Envelope, error), start func(context.Context, string, hostBootstrap) error) error {
	// Start the budget before reading bootstrap; nested observation/start
	// contexts inherit this deadline and cannot grant a second window.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	bootstrap, err := loadHostBootstrap(capability)
	if err != nil {
		return err
	}
	if receipt.Mode != "successor" || receipt.CommandID == "" || bootstrap.Hello.LaunchID != receipt.LaunchID || !validHostBootstrap(state, bootstrap) {
		return codeError("invalid_host_bootstrap")
	}
	return ensureSuccessorSourceHostPresence(ctx, receipt.CommandID, func(ctx context.Context) (contract.HostPresenceResponse, error) {
		response, err := rpc(ctx, state, ipc.KindHostStatus, coordinator.HostStatusRequest{LaunchID: bootstrap.Hello.LaunchID, LaunchToken: bootstrap.Hello.LaunchToken})
		if err != nil {
			return contract.HostPresenceResponse{}, err
		}
		var presence contract.HostPresenceResponse
		if len(response.Payload) > 64<<10 || contract.DecodeBridgeStrict(response.Payload, &presence) != nil {
			return presence, codeError("host_presence_invalid")
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(response.Payload, &fields) != nil {
			return presence, codeError("host_presence_invalid")
		}
		for _, key := range []string{"status", "presence", "execution_owner_mode", "owner_phase"} {
			if value, ok := fields[key]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return presence, codeError("host_presence_invalid")
			}
		}
		if value, ok := fields["successor_command_id"]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return presence, codeError("host_presence_invalid")
		}
		if presence.OwnerPhase == "succession_pending" && presence.SuccessorCommandID == "" {
			return presence, codeError("host_presence_invalid")
		}
		return presence, nil
	}, func(ctx context.Context) error {
		selected := bootstrap
		selected.Hello.ResumeCommandID = receipt.CommandID
		return start(ctx, state, selected)
	})
}

func ensureSuccessorSourceHost(ctx context.Context, state string, capability controlCapability, receipt coordinator.ResumeResponse) error {
	return ensureSuccessorSourceHostWith(ctx, state, capability, receipt, call, func(ctx context.Context, state string, bootstrap hostBootstrap) error {
		executable, err := resolvedExecutable()
		if err != nil {
			return err
		}
		return startSourceHostContext(ctx, executable, state, bootstrap)
	})
}
