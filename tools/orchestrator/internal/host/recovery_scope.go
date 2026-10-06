package host

import (
	"context"
	"encoding/json"
	"errors"
	"net"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

func openSourceRecovery(ctx context.Context, cfg SourceConfig, expected string) (*hostRecoveryState, error) {
	s, err := openHostRecoveryRecord(cfg.SpoolRoot, expected, cfg.Hello.PID, cfg.Hello.Birth, true)
	if err != nil {
		return nil, err
	}
	if s.record.ScopeSHA256 == expected {
		return s, nil
	}
	if err := transitionSourceRecovery(ctx, s, cfg.Hello, expected, func(ctx context.Context, req contract.HostRecoveryScopeRequest, expected string) error {
		dialCtx, cancel := context.WithTimeout(ctx, sourceOperationTimeout)
		defer cancel()
		conn, err := (&net.Dialer{}).DialContext(dialCtx, "unix", cfg.SocketPath)
		if err != nil {
			return err
		}
		defer conn.Close()
		return exchangeRecoveryScope(dialCtx, conn, req, expected)
	}); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

// Proof is read-only and cannot restart a coordinator or register a Host.
// A lost/rejected ACK leaves every retained file byte and counter unchanged.
func transitionSourceRecovery(ctx context.Context, s *hostRecoveryState, hello contract.HostHello, expected string, verify func(context.Context, contract.HostRecoveryScopeRequest, string) error) error {
	prior := s.record.ScopeSHA256
	if err := verify(ctx, contract.HostRecoveryScopeRequest{Hello: hello, PriorScopeSHA256: prior}, expected); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.transitionScope(prior, expected)
}

func exchangeRecoveryScope(ctx context.Context, conn net.Conn, req contract.HostRecoveryScopeRequest, expected string) error {
	s := &sourceSession{conn: conn}
	defer s.fail(net.ErrClosed)
	requestID := req.Hello.LaunchID + ":scope:" + req.PriorScopeSHA256
	payload, _ := json.Marshal(req)
	if err := s.writeEnvelope(ctx, ipc.KindHostRecoveryScope, requestID, payload); err != nil {
		return err
	}
	response, err := s.readEnvelope(ctx)
	if err != nil {
		return err
	}
	var ack contract.HostRecoveryScopeAck
	if response.Kind != ipc.KindResponse || response.RequestID != requestID || response.Epoch == 0 || decodePayload(response.Payload, &ack) != nil || ack.Status != "verified_scope_transition" || ack.LaunchID != req.Hello.LaunchID || ack.PriorScopeSHA256 != req.PriorScopeSHA256 || ack.ScopeSHA256 != expected {
		return errors.New("host_recovery_scope_transition_rejected")
	}
	return nil
}
