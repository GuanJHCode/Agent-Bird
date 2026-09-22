package host

import (
	"context"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func codexSandboxCommand(ctx context.Context, cmd process.Command, profile *adapter.ExecutionProfile, scratch string, home *codexHome) (process.Command, error) {
	return sandboxCommandWithPolicy(ctx, cmd, profile, scratch, nil, home)
}
