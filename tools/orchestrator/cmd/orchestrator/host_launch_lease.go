//go:build !windows

package main

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/hostbootstrap"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

type hostLaunchLease struct{ *hostbootstrap.Lease }

func openHostLaunchLease(ctx context.Context, state string, c controlCapability) (*hostLaunchLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.Version != 2 || c.RunID == "" || c.HostLaunchToken == "" || c.BootstrapPath != filepath.Join(state, "host-bootstrap", c.HostLaunchID+".json") {
		return nil, codeError("host_launch_binding_mismatch")
	}
	lease, err := hostbootstrap.Open(ctx, state, c.HostLaunchID)
	if err != nil {
		return nil, hostLaunchLeaseError(err)
	}
	return &hostLaunchLease{lease}, nil
}
func hostLaunchLeaseError(err error) error {
	var coded hostbootstrap.Error
	if errors.As(err, &coded) {
		return codeError(coded)
	}
	return err
}
func (l *hostLaunchLease) close()           { l.Close() }
func (l *hostLaunchLease) unchanged() error { return hostLaunchLeaseError(l.Unchanged()) }
func (l *hostLaunchLease) writeRecoveryIntent(v any) error {
	return hostLaunchLeaseError(l.WriteRecoveryIntent(v))
}
func (l *hostLaunchLease) publish(b hostBootstrap) error {
	return hostLaunchLeaseError(l.Publish(bootstrapMetadata(b)))
}
func (l *hostLaunchLease) read(c controlCapability) (hostBootstrap, error) {
	m, err := l.Read()
	if err != nil {
		return hostBootstrap{}, hostLaunchLeaseError(err)
	}
	if m.Hello.LaunchID != c.HostLaunchID {
		return hostBootstrap{}, codeError("host_launch_binding_mismatch")
	}
	h := m.Hello
	b := hostBootstrap{OwnerLaunchProtocol: m.OwnerLaunchProtocol, Version: 2, SocketPath: m.SocketPath, SpoolRoot: m.SpoolRoot, ProducerID: m.ProducerID}
	b.Hello.LaunchID, b.Hello.LaunchToken = c.HostLaunchID, c.HostLaunchToken
	b.Hello.OriginContextID, b.Hello.OriginPID, b.Hello.OriginBirth = h.OriginContextID, h.OriginPID, h.OriginBirth
	b.Hello.HostGeneration, b.Hello.PID, b.Hello.Birth, b.Hello.Executable = h.HostGeneration, h.PID, h.Birth, h.Executable
	return b, nil
}

func rebindHostBootstrap(ctx context.Context, state string, c controlCapability, req coordinator.RebindOwnerRequest) (ipc.Envelope, error) {
	l, err := openHostLaunchLease(ctx, state, c)
	if err != nil {
		return ipc.Envelope{}, err
	}
	defer l.close()
	b, err := l.read(c)
	if err != nil {
		return ipc.Envelope{}, err
	}
	if req.OwnerMode == "local" {
		req.OriginContextID = b.Hello.OriginContextID
	}
	if req.OriginContextID != b.Hello.OriginContextID {
		return ipc.Envelope{}, codeError("host_launch_binding_mismatch")
	}
	if err = l.unchanged(); err != nil {
		return ipc.Envelope{}, err
	}
	rpc, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, err := call(rpc, state, ipc.KindRebindOwner, req)
	if err != nil {
		return ipc.Envelope{}, err
	}
	var receipt struct {
		Version         int    `json:"version"`
		Status          string `json:"status"`
		RunID           string `json:"run_id"`
		OriginContextID string `json:"origin_context_id"`
		HostGeneration  string `json:"host_generation"`
	}
	if decode(response.Payload, &receipt) != nil || receipt.Version != 1 || receipt.Status != "owner_rebound" || receipt.RunID != c.RunID || receipt.OriginContextID != req.OriginContextID || receipt.HostGeneration != req.HostGeneration {
		return ipc.Envelope{}, codeError("invalid_rebind_response")
	}
	if err = ctx.Err(); err != nil {
		return ipc.Envelope{}, err
	}
	b.Hello.OriginPID, b.Hello.OriginBirth, b.Hello.OriginContextID, b.Hello.HostGeneration = req.OriginPID, req.OriginBirth, req.OriginContextID, req.HostGeneration
	if err = l.publish(b); err != nil {
		return ipc.Envelope{}, err
	}
	return response, nil
}
