package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/diagnostics"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

func receiptForDiagnosticBody(body DiagnosticsBody) DiagnosticsReceipt {
	raw, _ := json.Marshal(body)
	return DiagnosticsReceipt{Identity: body.Identity, Start: body.Start, Next: body.Next, Gap: body.Gap, BodySHA256: runtimeHash(string(raw))}
}

func encodeDiagnosticBody(body DiagnosticsBody) ([]byte, error) {
	envelope := DiagnosticsEnvelope{Version: 1, Body: body, Receipt: receiptForDiagnosticBody(body)}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	if len(raw) > DiagnosticsEnvelopeBytes {
		return nil, CodeError("diagnostics_page_too_large")
	}
	// Bound the actual transport encoding as well, before the durable pending
	// page is accepted. Handler enforces this same ASCII request-ID bound.
	wire, err := json.Marshal(ipc.Envelope{Version: ipc.Version, Kind: ipc.KindResponse, RequestID: strings.Repeat("x", 128), Epoch: math.MaxUint64, Payload: json.RawMessage(raw)})
	if err != nil || len(wire) > ipc.MaxMessageSize {
		return nil, CodeError("diagnostics_page_too_large")
	}
	return raw, nil
}

func unavailableDiagnosticEnvelope(f diagnosticReadFence) ([]byte, error) {
	return encodeDiagnosticBody(DiagnosticsBody{Identity: f.identity, Status: "unavailable", Error: "diagnostics_unavailable", Start: f.start, Next: f.start, Records: []DiagnosticsRecord{}})
}

func loadDiagnosticEnvelope(ctx context.Context, f diagnosticReadFence) ([]byte, error) {
	reader, err := diagnostics.OpenReader(ctx, f.root, f.scope)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return unavailableDiagnosticEnvelope(f)
	}
	var raw []byte
	for records := 64; records >= 1; records /= 2 {
		page, readErr := reader.ReadBounded(ctx, f.start, diagnostics.RecordBytes, records)
		if readErr != nil {
			err = readErr
			break
		}
		body := DiagnosticsBody{Identity: f.identity, Status: "available", Start: f.start, Next: page.Next, Gap: page.Gap, Snapshot: &page.Snapshot, Records: make([]DiagnosticsRecord, 0, len(page.Records))}
		for _, rec := range page.Records {
			data := rec.Data
			if data == nil {
				data = []byte{}
			}
			body.Records = append(body.Records, DiagnosticsRecord{Stream: rec.Stream, SegmentID: rec.SegmentID, Offset: rec.Offset, LostBefore: rec.LostBefore, Data: data})
		}
		raw, err = encodeDiagnosticBody(body)
		if err == nil {
			break
		}
		if err != CodeError("diagnostics_page_too_large") {
			break
		}
	}
	closeErr := reader.Close()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || closeErr != nil {
		return unavailableDiagnosticEnvelope(f)
	}
	return raw, nil
}

func validateDiagnosticEnvelope(raw []byte, f diagnosticReadFence) (envelope DiagnosticsEnvelope, control int64, err error) {
	bad := CodeError("diagnostics_state_invalid")
	if len(raw) == 0 || len(raw) > DiagnosticsEnvelopeBytes || contract.DecodeBridgeStrict(raw, &envelope) != nil || envelope.Version != 1 || envelope.Body.Identity != f.identity || envelope.Body.Start != f.start || envelope.Receipt.Validate() != nil || envelope.Body.Next.ScopeSHA256 != f.start.ScopeSHA256 || len(envelope.Body.Records) > 64 {
		return envelope, 0, bad
	}
	canonical, encodeErr := encodeDiagnosticBody(envelope.Body)
	if encodeErr != nil || !bytes.Equal(canonical, raw) {
		return envelope, 0, bad
	}
	b := envelope.Body
	if b.Records == nil {
		return envelope, 0, bad
	}
	if b.Status == "unavailable" {
		if b.Error != "diagnostics_unavailable" || b.Snapshot != nil || len(b.Records) != 0 || b.Gap != nil || b.Next != b.Start {
			return envelope, 0, bad
		}
	} else if b.Status != "available" || b.Error != "" || b.Snapshot == nil {
		return envelope, 0, bad
	}
	rawBytes, encodedBytes := 0, 0
	for _, r := range b.Records {
		if r.Stream != diagnostics.Stdout && r.Stream != diagnostics.Stderr || r.Data == nil || r.SegmentID == "" || len(r.SegmentID) > 128 || r.LostBefore > r.Offset || len(r.Data) == 0 && r.LostBefore == 0 || uint64(len(r.Data)) > math.MaxUint64-r.Offset {
			return envelope, 0, bad
		}
		rawBytes += len(r.Data)
		encodedBytes += base64.StdEncoding.EncodedLen(len(r.Data))
	}
	if rawBytes > diagnostics.RecordBytes {
		return envelope, 0, bad
	}
	return envelope, int64(len(raw) - encodedBytes), nil
}
