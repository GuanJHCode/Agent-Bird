package host

import (
	"context"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func callOwnerRuntimeOperation(ctx context.Context, rpc ownerExecutionRPC, revision uint64, kind, id string, body any) (contract.OwnerRPCReply, error) {
	op, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return rpc(op, revision, kind, id, body)
}
