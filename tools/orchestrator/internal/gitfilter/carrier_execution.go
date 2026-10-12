package gitfilter

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
)

// CarrierExecutionRequest is a live Go-only join. The authenticated original
// Host supplies Helper from its signed original-owner scope, never argv or a
// freshly observed executable. Broker is the original live segment owner.
type CarrierExecutionRequest struct {
	Launch   contract.LaunchCommand
	Manifest contract.GitFilterManifestV1
	Broker   ResourceBroker
	Spool    *events.Spool
	Permits  []ExactTransformPermit
	Helper   CarrierHelperExecutableExpectation
}

type CarrierExecution struct {
	self  *CarrierExecution
	state *carrierExecutionState
}
