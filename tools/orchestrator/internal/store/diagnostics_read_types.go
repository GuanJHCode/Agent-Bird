package store

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/diagnostics"
)

const DiagnosticsEnvelopeBytes = 60 << 10

type DiagnosticsReadRequest struct {
	Version          int                     `json:"version"`
	TaskID           string                  `json:"task_id"`
	AttemptID        string                  `json:"attempt_id"`
	RequestID        string                  `json:"request_id"`
	ExpectedRevision int64                   `json:"expected_revision"`
	Cursor           *diagnostics.PageCursor `json:"cursor,omitempty"`
}

type DiagnosticsIdentity struct {
	Version       int    `json:"version"`
	RunID         string `json:"run_id"`
	TaskID        string `json:"task_id"`
	AttemptID     string `json:"attempt_id"`
	ProducerID    string `json:"producer_id"`
	ReservationID string `json:"reservation_id"`
	PolicySHA256  string `json:"policy_sha256"`
	Revision      int64  `json:"revision"`
	RequestID     string `json:"request_id"`
	DeliveryID    string `json:"delivery_id"`
}

type DiagnosticsRecord struct {
	Stream     diagnostics.Stream `json:"stream"`
	SegmentID  string             `json:"segment_id"`
	Offset     uint64             `json:"offset"`
	LostBefore uint64             `json:"lost_before"`
	Data       []byte             `json:"data"`
}

type DiagnosticsBody struct {
	Identity DiagnosticsIdentity    `json:"identity"`
	Status   string                 `json:"status"`
	Error    string                 `json:"error,omitempty"`
	Start    diagnostics.PageCursor `json:"start"`
	Next     diagnostics.PageCursor `json:"next"`
	Gap      *diagnostics.PageGap   `json:"gap,omitempty"`
	Records  []DiagnosticsRecord    `json:"records"`
	Snapshot *diagnostics.Snapshot  `json:"snapshot,omitempty"`
}

type DiagnosticsReceipt struct {
	Identity   DiagnosticsIdentity    `json:"identity"`
	Start      diagnostics.PageCursor `json:"start"`
	Next       diagnostics.PageCursor `json:"next"`
	Gap        *diagnostics.PageGap   `json:"gap,omitempty"`
	BodySHA256 string                 `json:"body_sha256"`
}

type DiagnosticsEnvelope struct {
	Version int                `json:"version"`
	Body    DiagnosticsBody    `json:"body"`
	Receipt DiagnosticsReceipt `json:"receipt"`
}

type DiagnosticsAckResult struct {
	Version      int                `json:"version"`
	Status       string             `json:"status"`
	Receipt      DiagnosticsReceipt `json:"receipt"`
	NextRevision int64              `json:"next_revision"`
}

// This bound also applies to the outer IPC request ID, whose JSON encoding
// must fit the persisted envelope's explicitly measured transport headroom.
func ValidDiagnosticsToken(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func validDiagnosticsCursor(c diagnostics.PageCursor) bool {
	return validDigest(c.ScopeSHA256) && c.RecordOffset >= 0 && c.RecordOffset <= diagnostics.ChunkBytes-32 && c.DataOffset >= 0 && c.DataOffset < diagnostics.RecordBytes && (c.ChunkSequence != 0 || c.RecordOffset == 0 && c.DataOffset == 0)
}

func (r DiagnosticsReadRequest) Validate() error {
	if r.Version != 1 || len(r.TaskID) < 1 || len(r.TaskID) > 256 || !utf8.ValidString(r.TaskID) || bytes.ContainsRune([]byte(r.TaskID), 0) || !ValidDiagnosticsToken(r.AttemptID) || !ValidDiagnosticsToken(r.RequestID) || r.ExpectedRevision < 1 || r.ExpectedRevision == math.MaxInt64 || r.Cursor != nil && !validDiagnosticsCursor(*r.Cursor) {
		return CodeError("diagnostics_request_invalid")
	}
	return nil
}

func (r DiagnosticsReceipt) Validate() error {
	i := r.Identity
	scope := diagnostics.Scope{Version: 1, RunID: i.RunID, TaskID: i.TaskID, AttemptID: i.AttemptID, ProducerID: i.ProducerID, PolicySHA256: i.PolicySHA256}
	hash, err := scope.Hash()
	if err != nil || i.Version != 1 || i.Revision < 1 || i.Revision == math.MaxInt64 || !ValidDiagnosticsToken(i.AttemptID) || !ValidDiagnosticsToken(i.ProducerID) || !ValidDiagnosticsToken(i.RequestID) || !ValidDiagnosticsToken(i.ReservationID) || !ValidDiagnosticsToken(i.DeliveryID) || !validDigest(r.BodySHA256) || !validDiagnosticsCursor(r.Start) || !validDiagnosticsCursor(r.Next) || r.Start.ScopeSHA256 != hash || r.Next.ScopeSHA256 != hash {
		return CodeError("diagnostics_receipt_invalid")
	}
	if r.Gap != nil && (!validDiagnosticsCursor(r.Gap.From) || !validDiagnosticsCursor(r.Gap.To) || r.Gap.From != r.Start || r.Gap.To.ScopeSHA256 != hash) {
		return CodeError("diagnostics_receipt_invalid")
	}
	return nil
}

func strictDiagnosticsJSON(raw []byte, target any) error {
	if err := contract.DecodeBridgeStrict(raw, target); err != nil {
		return CodeError("diagnostics_request_invalid")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		token, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil || token == nil {
			return CodeError("diagnostics_request_invalid")
		}
	}
}

func (r *DiagnosticsReadRequest) UnmarshalJSON(raw []byte) error {
	type plain DiagnosticsReadRequest
	var v plain
	if err := strictDiagnosticsJSON(raw, &v); err != nil {
		return err
	}
	next := DiagnosticsReadRequest(v)
	if err := next.Validate(); err != nil {
		return err
	}
	*r = next
	return nil
}

func (r *DiagnosticsReceipt) UnmarshalJSON(raw []byte) error {
	type plain DiagnosticsReceipt
	var v plain
	if err := strictDiagnosticsJSON(raw, &v); err != nil {
		return err
	}
	next := DiagnosticsReceipt(v)
	if err := next.Validate(); err != nil {
		return err
	}
	*r = next
	return nil
}
