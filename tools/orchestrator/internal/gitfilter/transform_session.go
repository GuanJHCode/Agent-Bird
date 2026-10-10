package gitfilter

import (
	"context"
	"errors"
	"io"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

var ErrExactTransformSession = errors.New("exact_filter_transform_session_invalid")

type ExactTransformPermit struct {
	Path, Operation, InputSHA256, OutputSHA256 string
	InputBytes, OutputBytes                    int64
	MaximumCalls                               uint16
}

// These observations cannot reconstruct a session or a Broker.
type ExactTransformRecord struct {
	Phase                                                     string
	Sequence                                                  uint64
	ManifestSHA256, ScopeSHA256, ReservationID, AttemptID     string
	PlanRevision                                              int
	PermitsSHA256, Path, Operation, InputSHA256, OutputSHA256 string
	InputBytes, OutputBytes                                   int64
	Started, WaitProved, TreeProved, Unknown                  bool
}

type ExactTransformJournal interface {
	RecordExactTransform(context.Context, ExactTransformRecord) error
}

type ExactTransformState struct {
	Calls                             uint64
	InFlight, Failed, Unknown, Closed bool
}

type ExactTransformSession struct{}

func OpenExactTransformSession(broker ResourceBroker, expected StreamBinding, manifest contract.GitFilterManifestV1, allowed []ExactTransformPermit, journal ExactTransformJournal) (*ExactTransformSession, error) {
	return nil, ErrExactTransformSession
}

func (s *ExactTransformSession) Exchange(ctx context.Context, sequence uint64, path, operation string, produce func(io.Writer) error) ([]byte, error) {
	return nil, ErrExactTransformSession
}

func (s *ExactTransformSession) State() ExactTransformState { return ExactTransformState{} }
func (s *ExactTransformSession) Close() error               { return nil }
