//go:build darwin

package gitfilter

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
)

// BrokerRequest is Go-only: the original Host must authenticate Launch and the
// separate resource Grant before constructing it. It is never a public request.
type BrokerRequest struct {
	Launch           contract.LaunchCommand
	Grant            contract.GitFilterResourceGrant
	Manifest         contract.GitFilterManifestV1
	SegmentDirectory string
	Spool            *events.Spool
}

type BrokerState struct {
	StartedAt, TerminalAt time.Time
	EverStarted, Unknown  bool
}

type Broker struct {
	slots    [3]*os.File
	resource string
}

func NewBroker(context.Context, BrokerRequest) (*Broker, error) {
	return nil, errors.New("git_filter_broker_not_implemented")
}
func (b *Broker) Load(context.Context, int, *os.File) error {
	return errors.New("git_filter_broker_not_implemented")
}
func (b *Broker) Transform(context.Context, string, int, int) (Result, error) {
	return Result{}, errors.New("git_filter_broker_not_implemented")
}
func (b *Broker) ReadSlot(context.Context, int, io.Writer) error {
	return errors.New("git_filter_broker_not_implemented")
}
func (b *Broker) State() BrokerState { return BrokerState{} }
func (b *Broker) Cleanup(context.Context) (*contract.GitFilterCleanup, error) {
	return nil, errors.New("git_filter_broker_not_implemented")
}
func (b *Broker) Close() error { return nil }
