package ipc

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// OwnerClient never receives a launch/control token. Every call opens a fresh
// kernel-authenticated channel and verifies a reply bound to its full request.
type OwnerClient struct {
	SocketPath     string
	Intent         contract.OriginalOwnerIntent
	Owner          contract.OriginalOwnerIdentity
	PrivateKey     ed25519.PrivateKey
	CoordinatorKey ed25519.PublicKey
}

func (c OwnerClient) Call(ctx context.Context, revision uint64, kind, requestID string, body any) (contract.OwnerRPCReply, error) {
	var zero contract.OwnerRPCReply
	if !filepath.IsAbs(c.SocketPath) || c.Intent.Validate() != nil || c.Owner.Validate() != nil || len(c.PrivateKey) != ed25519.PrivateKeySize || hex.EncodeToString(c.PrivateKey.Public().(ed25519.PublicKey)) != c.Owner.PublicKey {
		return zero, ErrInvalidMessage
	}
	id, err := contract.OriginalOwnerKeyID(c.CoordinatorKey)
	if err != nil || id != c.Intent.CoordinatorKeyID {
		return zero, ErrInvalidMessage
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return zero, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	open, _ := json.Marshal(contract.OwnerRPCOpen{Version: 1, OwnerID: c.Intent.Scope.OwnerID, Owner: c.Owner})
	if err = Write(conn, Envelope{Version: Version, Kind: KindOwnerRPC, RequestID: requestID, Payload: open}); err != nil {
		return zero, err
	}
	frame, err := Read(conn)
	if err != nil {
		return zero, err
	}
	challenge, err := contract.DecodeOwnerChallenge(frame.Payload)
	if err != nil || frame.Kind != KindOwnerChallenge || frame.RequestID != requestID || frame.Epoch != challenge.CoordinatorEpoch || contract.VerifyOwnerChallenge(challenge, c.CoordinatorKey, c.Intent.Scope.OwnerID) != nil || challenge.CoordinatorEpoch < c.Intent.CoordinatorEpoch {
		return zero, errors.New("original_owner_challenge_rejected")
	}
	scope, _ := contract.OriginalOwnerScopeSHA256(c.Intent.Scope)
	identity, _ := contract.OriginalOwnerIdentitySHA256(c.Owner)
	raw, err := json.Marshal(body)
	if err != nil {
		return zero, err
	}
	request, err := contract.SignOwnerRPCRequest(contract.OwnerRPCRequest{Version: 1, OwnerID: c.Intent.Scope.OwnerID, ScopeSHA256: scope, OwnerIdentitySHA256: identity, CoordinatorEpoch: challenge.CoordinatorEpoch, ControlRevision: revision, RequestID: requestID, Kind: kind, Nonce: challenge.Nonce, Body: raw}, c.PrivateKey)
	if err != nil {
		return zero, err
	}
	raw, _ = json.Marshal(request)
	if err = Write(conn, Envelope{Version: Version, Kind: KindOwnerRPC, RequestID: requestID, Epoch: challenge.CoordinatorEpoch, Payload: raw}); err != nil {
		return zero, err
	}
	frame, err = Read(conn)
	if err != nil {
		return zero, err
	}
	reply, err := contract.DecodeOwnerRPCReply(frame.Payload)
	if err != nil || frame.Kind != KindResponse || frame.RequestID != requestID || frame.Epoch != challenge.CoordinatorEpoch || contract.VerifyOwnerRPCReply(reply, c.CoordinatorKey, request) != nil {
		return zero, errors.New("original_owner_reply_rejected")
	}
	if reply.Error != "" {
		return reply, errors.New(reply.Error)
	}
	return reply, nil
}
