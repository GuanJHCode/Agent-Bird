package gitfilter

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
)

// BrokerRequest is Go-only. The original Host authenticates Launch and the
// independent Grant before construction; this API does not grant authority.
type BrokerRequest struct {
	Launch           contract.LaunchCommand
	Grant            contract.GitFilterResourceGrant
	Manifest         contract.GitFilterManifestV1
	SegmentDirectory string
	Spool            *events.Spool
}

type BrokerState struct {
	StartedAt, TerminalAt time.Time `json:"-"`
	EverStarted, Unknown  bool      `json:"-"`
}

// StreamLoadResult is live-only slot evidence, never durable wire data.
type StreamLoadResult struct {
	SHA256 string `json:"-"`
	Bytes  int64  `json:"-"`
}

// StreamBinding observes the original live resource owner. It cannot recreate
// a Broker, authenticate a grant, or authorize cleanup from serialized data.
type StreamBinding struct {
	ManifestSHA256 string `json:"-"`
	ScopeSHA256    string `json:"-"`
	ReservationID  string `json:"-"`
	AttemptID      string `json:"-"`
	PlanRevision   int    `json:"-"`
}

// ResourceBroker is a live, original-owner API. No operation recreates a
// broker from its journal, and Close never removes its retained resources.
type ResourceBroker interface {
	Binding() StreamBinding
	Load(context.Context, int, *os.File) error
	LoadStream(context.Context, int, func(io.Writer) error) (StreamLoadResult, error)
	Transform(context.Context, string, int, int) (Result, error)
	ReadSlot(context.Context, int, io.Writer) error
	State() BrokerState
	Cleanup(context.Context) (*contract.GitFilterCleanup, error)
	Close() error
}
