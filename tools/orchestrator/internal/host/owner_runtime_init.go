package host

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

// Only fresh construction checks the startup deadline. The surviving object
// keeps its key and exact Hello for a committed Bind's later ACK repair.
func newOwnerEnrollment(parent context.Context, bootstrap contract.OwnerRuntimeBootstrap) (*ownerEnrollment, error) {
	if bootstrap.Validate() != nil || bootstrap.Prepared.Intent.StartupDeadlineUnixMS <= time.Now().UnixMilli() {
		return nil, errOwnerEnrollment
	}
	deadline := time.Now().Add(5 * time.Second)
	if startup := time.UnixMilli(bootstrap.Prepared.Intent.StartupDeadlineUnixMS); startup.Before(deadline) {
		deadline = startup
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parentIdentity := bootstrap.Prepared.Intent.FirstHost
	if bootstrap.Version == 2 {
		receipt := bootstrap.LaunchReceipt
		if receipt == nil || receipt.Child == nil || receipt.Child.PID != os.Getpid() {
			return nil, errOwnerEnrollment
		}
		parentIdentity = contract.OriginalOwnerTransport{PID: receipt.Supervisor.PID, Birth: receipt.Supervisor.Birth}
	}
	physical, err := ownerProcessIdentity(ctx, bootstrap.StateDir, bootstrap.Prepared.Intent.Scope, parentIdentity)
	if err != nil {
		return nil, err
	}
	if bootstrap.Version == 2 && physical != *bootstrap.LaunchReceipt.Child {
		return nil, errOwnerEnrollment
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	coordinator, err := hex.DecodeString(bootstrap.Prepared.CoordinatorPublicKey)
	if err != nil {
		return nil, errOwnerEnrollment
	}
	owner := contract.OriginalOwnerIdentity{PID: physical.PID, Birth: physical.Birth, PublicKey: hex.EncodeToString(public)}
	return &ownerEnrollment{bootstrap: bootstrap, client: ipc.OwnerClient{SocketPath: bootstrap.Prepared.RPCSocketPath, Intent: bootstrap.Prepared.Intent, Owner: owner, PrivateKey: private, CoordinatorKey: ed25519.PublicKey(coordinator)}}, nil
}
