//go:build !darwin

package gitfilter

import (
	"context"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type carrierExecutionState struct{}

func OpenCarrierExecution(context.Context, CarrierExecutionRequest) (*CarrierExecution, error) {
	return nil, ErrCarrierHelper
}
func (*CarrierExecution) GitConfigArgs() []string { return nil }
func (*CarrierExecution) BindCommand(context.Context, string) (string, error) {
	return "", ErrCarrierHelper
}
func (*CarrierExecution) Activate(context.Context, *process.Handle, string) error {
	return ErrCarrierHelper
}
func (*CarrierExecution) Drain(context.Context) error                   { return ErrCarrierHelper }
func (*CarrierExecution) Remove(context.Context, *process.Handle) error { return ErrCarrierHelper }
func (*CarrierExecution) Cleanup(context.Context) (*contract.GitFilterCarrierCleanup, error) {
	return nil, ErrCarrierHelper
}
func (*CarrierExecution) Close() error { return ErrCarrierHelper }
