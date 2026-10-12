package gitfilter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

var ErrExactTransformSession = errors.New("exact_filter_transform_session_invalid")

type ExactTransformPermit struct {
	Path, Operation, InputSHA256, OutputSHA256 string
	InputBytes, OutputBytes                    int64
	MaximumCalls                               uint16
}

// Scalar observations cannot reconstruct a session or a Broker.
type ExactTransformRecord struct {
	Phase, Outcome                                            string
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

// ExactTransformSession is live original-owner state, never wire authority.
// Close only revokes the session, never closes/cleans the borrowed Broker.
type ExactTransformSession struct {
	self         *ExactTransformSession
	mu           sync.Mutex
	broker       ResourceBroker
	binding      StreamBinding
	observation  BrokerObservation
	journal      ExactTransformJournal
	permits      []ExactTransformPermit
	used         []uint16
	permitsSHA   string
	maximumCalls uint64
	state        ExactTransformState
}

func OpenExactTransformSession(broker ResourceBroker, expected StreamBinding, manifest contract.GitFilterManifestV1, allowed []ExactTransformPermit, journal ExactTransformJournal) (*ExactTransformSession, error) {
	if exactNil(broker) || exactNil(journal) || len(allowed) == 0 || len(allowed) > 1024 || expected.AttemptID == "" || expected.ReservationID == "" || expected.PlanRevision < 1 || !exactDigest(expected.ScopeSHA256) {
		return nil, ErrExactTransformSession
	}
	manifest = manifest.Clone()
	digest, err := manifest.Digest()
	if err != nil || digest != expected.ManifestSHA256 {
		return nil, ErrExactTransformSession
	}
	paths := make(map[string]bool, len(manifest.Paths))
	for _, p := range manifest.Paths {
		paths[p] = true
	}
	allowed = append([]ExactTransformPermit(nil), allowed...)
	var total uint64
	for _, p := range allowed {
		if !paths[p.Path] || p.Operation != "clean" && p.Operation != "smudge" || !exactDigest(p.InputSHA256) || !exactDigest(p.OutputSHA256) || p.InputBytes < 0 || p.InputBytes > MaximumBytes || p.OutputBytes < 0 || p.OutputBytes > MaximumBytes || p.MaximumCalls < 1 || p.MaximumCalls > 16 {
			return nil, ErrExactTransformSession
		}
		total += uint64(p.MaximumCalls)
		if total > 1024 {
			return nil, ErrExactTransformSession
		}
	}
	sort.Slice(allowed, func(i, j int) bool {
		a, b := allowed[i], allowed[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Operation != b.Operation {
			return a.Operation < b.Operation
		}
		if a.InputSHA256 != b.InputSHA256 {
			return a.InputSHA256 < b.InputSHA256
		}
		return a.InputBytes < b.InputBytes
	})
	for i := 1; i < len(allowed); i++ {
		a, b := allowed[i-1], allowed[i]
		if a.Path == b.Path && a.Operation == b.Operation && a.InputSHA256 == b.InputSHA256 && a.InputBytes == b.InputBytes {
			return nil, ErrExactTransformSession
		}
	}
	raw, _ := json.Marshal(allowed)
	h := sha256.Sum256(append([]byte("exact-filter-transform-permits-v1\x00"), raw...))
	s := &ExactTransformSession{broker: broker, binding: expected, observation: broker.Observation(), journal: journal, permits: allowed, used: make([]uint16, len(allowed)), maximumCalls: total, permitsSHA: hex.EncodeToString(h[:])}
	s.self = s
	if err = s.ownerGuard(); err != nil {
		return nil, err
	}
	return s, nil
}

// produce is synchronous and must honor its deadline and writer errors.
// The future IPC adapter must interrupt blocked reads itself; an arbitrary
// callback cannot be turned into process exit proof by this leaf.
func (s *ExactTransformSession) Exchange(ctx context.Context, sequence uint64, path, operation string, produce func(io.Writer) error) (output []byte, retErr error) {
	if s == nil || s.self != s {
		return nil, ErrExactTransformSession
	}
	s.mu.Lock()
	valid := ctx != nil && produce != nil && !s.state.Closed && !s.state.Failed && !s.state.InFlight && sequence == s.state.Calls+1 && s.state.Calls < s.maximumCalls
	if valid {
		_, valid = ctx.Deadline()
		valid = valid && ctx.Err() == nil
	}
	available := false
	for i, p := range s.permits {
		if p.Path == path && p.Operation == operation && s.used[i] < p.MaximumCalls {
			available = true
			break
		}
	}
	if !valid || !available {
		s.state.Failed = true
		s.mu.Unlock()
		return nil, ErrExactTransformSession
	}
	s.state.InFlight = true
	s.mu.Unlock()
	mutated, terminal, committed := false, false, false
	defer func() {
		panicValue := recover()
		s.mu.Lock()
		if !committed {
			s.state.InFlight = false
			s.state.Failed = true
			output = nil
			if panicValue != nil || mutated && !terminal {
				s.state.Unknown = true
			}
		}
		s.mu.Unlock()
		if panicValue != nil {
			panic(panicValue)
		}
	}()
	record := ExactTransformRecord{Phase: "intent", Sequence: sequence, ManifestSHA256: s.binding.ManifestSHA256, ScopeSHA256: s.binding.ScopeSHA256, ReservationID: s.binding.ReservationID, AttemptID: s.binding.AttemptID, PlanRevision: s.binding.PlanRevision, PermitsSHA256: s.permitsSHA, Path: path, Operation: operation}
	if retErr = s.activeGuard(ctx); retErr != nil {
		return nil, retErr
	}
	retErr = s.journal.RecordExactTransform(ctx, record)
	retErr = errors.Join(retErr, s.activeGuard(ctx))
	if retErr != nil {
		return nil, retErr
	}
	mutated = true
	loaded, err := s.broker.LoadStream(ctx, 0, produce)
	record.InputSHA256, record.InputBytes = loaded.SHA256, loaded.Bytes
	retErr = errors.Join(err, s.observeStream(err, true), s.executionStateGuard(ctx))
	selected := -1
	if retErr == nil {
		for i, p := range s.permits {
			if p.Path == path && p.Operation == operation && p.InputSHA256 == loaded.SHA256 && p.InputBytes == loaded.Bytes {
				selected = i
				break
			}
		}
		s.mu.Lock()
		if selected < 0 || s.used[selected] >= s.permits[selected].MaximumCalls {
			retErr = ErrExactTransformSession
		} else {
			s.used[selected]++
		}
		s.mu.Unlock()
	}
	if retErr == nil {
		var result Result
		result, retErr = s.broker.Transform(ctx, operation, 0, 1)
		record.OutputSHA256, record.OutputBytes = result.OutputSHA256, result.OutputBytes
		record.Started, record.WaitProved, record.TreeProved = result.Started, result.WaitProved, result.TreeProved
		retErr = errors.Join(retErr, s.observeTransform(result, retErr), s.executionStateGuard(ctx))
		p := s.permits[selected]
		if !result.Accepted || !result.Started || result.ExitCode != 0 || result.ScopeSHA256 != s.binding.ScopeSHA256 || !exactDigest(result.RequestSHA256) || result.InputSHA256 != p.InputSHA256 || result.InputBytes != p.InputBytes || result.OutputSHA256 != p.OutputSHA256 || result.OutputBytes != p.OutputBytes {
			retErr = errors.Join(retErr, ErrExactTransformSession)
		}
	}
	if retErr == nil {
		p := s.permits[selected]
		out := &exactOutput{data: make([]byte, 0, int(p.OutputBytes)), limit: p.OutputBytes}
		err = s.broker.ReadSlot(ctx, 1, out)
		var readErr error
		output, readErr = out.take()
		retErr = errors.Join(err, readErr, s.observeStream(err, false), s.executionStateGuard(ctx))
		h := sha256.Sum256(output)
		if int64(len(output)) != p.OutputBytes || hex.EncodeToString(h[:]) != p.OutputSHA256 {
			retErr = errors.Join(retErr, ErrExactTransformSession)
		}
	}
	// Cancellation/revocation cannot suppress original tool terminal evidence.
	// Terminal records an exchange observation, not publication of bytes.
	finalCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	beforeTerminal := s.broker.Observation()
	guardErr := s.sameObservation(beforeTerminal)
	record.Phase = "terminal"
	record.Outcome = "accepted"
	if retErr != nil {
		record.Outcome = "rejected"
	}
	record.Unknown = s.State().Unknown
	if record.Unknown {
		record.Outcome = "unknown"
	}
	// Even loss of execution usability must produce an observation of the
	// original exchange. The frozen identity is not renewed execution authority.
	journalErr := s.journal.RecordExactTransform(finalCtx, record)
	if after := s.broker.Observation(); after != beforeTerminal {
		s.markUnknown()
		guardErr = errors.Join(guardErr, process.ErrProcessTreeUnknown)
	}
	if journalErr != nil {
		s.markUnknown()
	}
	terminal = journalErr == nil
	retErr = errors.Join(retErr, guardErr, journalErr)
	if retErr != nil {
		return nil, retErr
	}
	if retErr = s.activeGuard(ctx); retErr != nil {
		return nil, retErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Closed || s.state.Failed || s.state.Unknown || !s.state.InFlight || sequence != s.state.Calls+1 || ctx.Err() != nil {
		return nil, ErrExactTransformSession
	}
	// Success linearizes here; later Close cannot retract these owned bytes.
	s.state.Calls++
	s.state.InFlight = false
	committed = true
	return output, nil
}

// An atomic lifecycle observation preserves original identity after a known
// failure revokes Binding(). Only this call may advance the cached lifecycle.
func (s *ExactTransformSession) adoptObservation(next BrokerObservation) error {
	if next.OriginalBinding != s.binding || !next.Ready || next.Closed || next.Removed {
		s.markUnknown()
		return process.ErrProcessTreeUnknown
	}
	s.observation = next
	if next.State.Unknown {
		s.markUnknown()
		return process.ErrProcessTreeUnknown
	}
	return nil
}
func (s *ExactTransformSession) observeStream(callErr error, load bool) error {
	next := s.broker.Observation()
	prior := s.observation
	expected := prior
	if load && callErr != nil {
		expected.Failed = next.Failed
	}
	if next != expected {
		// Broker can mark I/O uncertainty, but never invent a known process start
		// during LoadStream/ReadSlot. Either way the session cannot continue.
		s.markUnknown()
		return errors.Join(process.ErrProcessTreeUnknown, s.adoptObservation(next))
	}
	return s.adoptObservation(next)
}
func (s *ExactTransformSession) observeTransform(r Result, callErr error) error {
	next := s.broker.Observation()
	before := s.observation
	unproven := r.Unknown || r.Started && (!r.WaitProved || !r.TreeProved || r.StartedAt.IsZero() || r.TerminalAt.IsZero() || r.TerminalAt.Before(r.StartedAt) || !before.State.TerminalAt.IsZero() && r.StartedAt.Before(before.State.TerminalAt))
	if unproven || next.State.Unknown {
		s.markUnknown()
		return errors.Join(process.ErrProcessTreeUnknown, s.adoptObservation(next))
	}
	expected := before
	if r.Started {
		expected.State.EverStarted = true
		if expected.State.StartedAt.IsZero() {
			expected.State.StartedAt = r.StartedAt
		}
		expected.State.TerminalAt = r.TerminalAt
	}
	if callErr != nil {
		expected.Failed = next.Failed
	} else {
		expected.Failed = !r.Accepted
	}
	if next != expected {
		s.markUnknown()
		return process.ErrProcessTreeUnknown
	}
	return s.adoptObservation(next)
}
func (s *ExactTransformSession) sameObservation(actual BrokerObservation) error {
	if actual != s.observation {
		s.markUnknown()
		return process.ErrProcessTreeUnknown
	}
	return nil
}
func (s *ExactTransformSession) ownerGuard() error {
	actual := s.broker.Observation()
	if err := s.sameObservation(actual); err != nil {
		return err
	}
	if actual.OriginalBinding != s.binding {
		s.markUnknown()
		return process.ErrProcessTreeUnknown
	}
	if !actual.Ready || actual.Failed || actual.Closed || actual.Removed || actual.State.Unknown {
		if actual.State.Unknown || actual.Closed || actual.Removed {
			s.markUnknown()
		}
		return ErrExactTransformSession
	}
	if s.broker.Binding() != s.binding {
		s.markUnknown()
		return process.ErrProcessTreeUnknown
	}
	return nil
}
func (s *ExactTransformSession) executionStateGuard(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Closed || s.state.Failed || s.state.Unknown || !s.state.InFlight {
		return ErrExactTransformSession
	}
	return nil
}
func (s *ExactTransformSession) activeGuard(ctx context.Context) error {
	return errors.Join(s.ownerGuard(), s.executionStateGuard(ctx))
}
func (s *ExactTransformSession) markUnknown() { s.mu.Lock(); s.state.Unknown = true; s.mu.Unlock() }
func (s *ExactTransformSession) State() ExactTransformState {
	if s == nil || s.self != s {
		return ExactTransformState{Closed: true, Failed: true}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}
func (s *ExactTransformSession) Close() error {
	if s == nil {
		return nil
	}
	if s.self != s {
		return ErrExactTransformSession
	}
	s.mu.Lock()
	s.state.Closed = true
	s.mu.Unlock()
	return nil
}
func exactNil(v any) bool {
	return v == nil || reflect.ValueOf(v).Kind() == reflect.Pointer && reflect.ValueOf(v).IsNil()
}
func exactDigest(s string) bool {
	if len(s) != 64 || s != strings.ToLower(s) {
		return false
	}
	_, e := hex.DecodeString(s)
	return e == nil
}

// The receiver is revoked before journal callbacks, so a retained writer
// cannot modify the private buffer that will be returned at success commit.
type exactOutput struct {
	mu     sync.Mutex
	data   []byte
	limit  int64
	err    error
	closed bool
}

func (w *exactOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.err != nil {
		return 0, ErrExactTransformSession
	}
	if int64(len(p)) > w.limit-int64(len(w.data)) {
		w.err = ErrExactTransformSession
		return 0, w.err
	}
	w.data = append(w.data, p...)
	return len(p), nil
}
func (w *exactOutput) take() ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	out := w.data
	w.data = nil
	return out, w.err
}
