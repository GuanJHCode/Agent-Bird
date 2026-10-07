package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/diagnostics"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

func TestDiagnosticsPublicReadConcurrentWinnerAndOutsideTransaction(t *testing.T) {
	f := newDiagnosticsReadFixture(t)
	f.append(t, 0, []byte("one"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish()
	type outcome struct {
		raw []byte
		err error
	}
	result := make(chan outcome, 1)
	go func() {
		raw, err := f.db.readDiagnostics(ctx, "controller", f.token, f.request, func(ctx context.Context, fence diagnosticReadFence) ([]byte, error) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return loadDiagnosticEnvelope(ctx, fence)
		})
		result <- outcome{raw, err}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("reader did not enter")
	}
	// A second public request requires the same DB mutex and transaction. It
	// must finish while the first filesystem operation is deliberately blocked.
	checked := make(chan outcome, 1)
	go func() {
		raw, err := f.db.ReadDiagnostics(ctx, "controller", f.token, f.request)
		checked <- outcome{raw, err}
	}()
	var winner outcome
	select {
	case winner = <-checked:
	case <-ctx.Done():
		t.Fatal("filesystem work retained database transaction/mutex")
	}
	if winner.err != nil {
		t.Fatal(winner.err)
	}
	other := f.request
	other.RequestID = "different"
	if _, err := f.db.ReadDiagnostics(ctx, "controller", f.token, other); err != ErrConflict {
		t.Fatal("pending was replaced", err)
	}
	f.append(t, 3, []byte("two"))
	finish()
	select {
	case loser := <-result:
		if loser.err != nil || !bytes.Equal(winner.raw, loser.raw) {
			t.Fatal("concurrent completion did not return exact persisted winner", loser.err)
		}
	case <-ctx.Done():
		t.Fatal("first read did not join")
	}
}

func TestDiagnosticsPublicReadRevalidatesAuthorityAfterFilesystem(t *testing.T) {
	for _, kind := range []string{"token", "deleting"} {
		t.Run(kind, func(t *testing.T) {
			f := newDiagnosticsReadFixture(t)
			f.append(t, 0, []byte("private"))
			ctx := context.Background()
			raw, err := f.db.readDiagnostics(ctx, "controller", f.token, f.request, func(ctx context.Context, fence diagnosticReadFence) ([]byte, error) {
				page, err := loadDiagnosticEnvelope(ctx, fence)
				if err != nil {
					return nil, err
				}
				if kind == "token" {
					_, err = f.db.sql.Exec(`UPDATE run_control SET token_hash=?`, runtimeHash("new-token"))
				} else {
					_, err = f.db.sql.Exec(`UPDATE managed_artifact_reservations SET state='deleting'`)
				}
				return page, err
			})
			if err == nil || len(raw) != 0 {
				t.Fatal("raw bytes escaped failed completion authorization", err)
			}
			var phase string
			var pending int
			if err = f.db.sql.QueryRow(`SELECT phase,length(pending_envelope) FROM diagnostic_delivery_state`).Scan(&phase, &pending); err != nil || phase != "reading" || pending != 0 {
				t.Fatal("failed completion changed fence", phase, pending, err)
			}
		})
	}
}

func TestDiagnosticsPublicReadCanceledFenceSurvivesReopen(t *testing.T) {
	f := newDiagnosticsReadFixture(t)
	f.append(t, 0, []byte("recover"))
	ctx, cancel := context.WithCancel(context.Background())
	_, err := f.db.readDiagnostics(ctx, "controller", f.token, f.request, func(context.Context, diagnosticReadFence) ([]byte, error) { cancel(); return nil, context.Canceled })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	path := f.db.path
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.db.ReadDiagnostics(context.Background(), "controller", f.token, f.request)
	var page DiagnosticsEnvelope
	if err != nil || json.Unmarshal(raw, &page) != nil || page.Receipt.Identity.Revision != 1 || len(page.Body.Records) != 1 || string(page.Body.Records[0].Data) != "recover" {
		t.Fatal("original reading fence was not resumed", err)
	}
	var next int64
	if err = f.db.sql.QueryRow(`SELECT next_revision FROM diagnostic_delivery_state`).Scan(&next); err != nil || next != 2 {
		t.Fatal("recovery burned a second revision", next, err)
	}
}

func TestDiagnosticsPublicUnavailableIsDurableAndACKable(t *testing.T) {
	f := newDiagnosticsReadFixture(t)
	ctx := context.Background()
	f.append(t, 0, []byte("later"))
	if err := f.writer.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.root, f.root+"-hidden"); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Rename(f.root+"-hidden", f.root) }()
	raw, err := f.db.ReadDiagnostics(ctx, "controller", f.token, f.request)
	var page DiagnosticsEnvelope
	if err != nil || json.Unmarshal(raw, &page) != nil || page.Body.Status != "unavailable" || page.Body.Error != "diagnostics_unavailable" || page.Body.Snapshot != nil || len(page.Body.Records) != 0 || page.Body.Start != page.Body.Next {
		t.Fatal("permanent failure lacks bounded ACKable unavailable page", err)
	}
	again, err := f.db.ReadDiagnostics(ctx, "controller", f.token, f.request)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatal("unavailable replay drift", err)
	}
	ack, err := f.db.AckDiagnostics(ctx, "controller", f.token, page.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(f.root+"-hidden", f.root); err != nil {
		t.Fatal(err)
	}
	f.request.ExpectedRevision, f.request.RequestID, f.request.Cursor = ack.NextRevision, "recovered", &page.Receipt.Next
	raw, err = f.db.ReadDiagnostics(ctx, "controller", f.token, f.request)
	if err != nil || json.Unmarshal(raw, &page) != nil || page.Body.Status != "available" || len(page.Body.Records) != 1 || string(page.Body.Records[0].Data) != "later" {
		t.Fatal("ACK of unavailable skipped data or blocked recovery", err)
	}
}

func TestDiagnosticsPublicEOFSameCursorRejectsOldRevision(t *testing.T) {
	f := newDiagnosticsReadFixture(t)
	ctx := context.Background()
	for revision := int64(1); revision <= 3; revision++ {
		req := f.request
		raw, err := f.db.ReadDiagnostics(ctx, "controller", f.token, req)
		var page DiagnosticsEnvelope
		if err != nil || json.Unmarshal(raw, &page) != nil || len(page.Body.Records) != 0 || page.Body.Start != page.Body.Next {
			t.Fatal("empty EOF cursor moved", err)
		}
		if _, err = f.db.AckDiagnostics(ctx, "controller", f.token, page.Receipt); err != nil {
			t.Fatal(err)
		}
		if _, err = f.db.ReadDiagnostics(ctx, "controller", f.token, req); err != ErrConflict {
			t.Fatal("old EOF request allocated another revision", err)
		}
		f.request.ExpectedRevision, f.request.RequestID, f.request.Cursor = revision+1, fmt.Sprint("read-", revision+1), &page.Receipt.Next
	}
}

func TestDiagnosticsPublicPayloadIsNotControlQuotaAndFailedPublishRetainsFence(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			f := newDiagnosticsReadFixture(t)
			f.append(t, 0, bytes.Repeat([]byte("d"), diagnostics.RecordBytes))
			ctx := context.Background()
			var before int64
			if err := f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			originalLimit := f.db.userControlLimit
			f.db.userControlLimit = before + 8192
			raw, err := f.db.readDiagnostics(ctx, "controller", f.token, f.request, func(ctx context.Context, fence diagnosticReadFence) ([]byte, error) {
				page, err := loadDiagnosticEnvelope(ctx, fence)
				if fail {
					f.db.userControlLimit = 1
				}
				return page, err
			})
			if fail {
				if err != CodeError("control_spool_full") || len(raw) != 0 {
					t.Fatal("quota failure exposed raw page", err)
				}
				var phase string
				var next int64
				if err = f.db.sql.QueryRow(`SELECT phase,next_revision FROM diagnostic_delivery_state`).Scan(&phase, &next); err != nil || phase != "reading" || next != 2 {
					t.Fatal("quota rollback lost or advanced original fence", err)
				}
				f.db.userControlLimit = originalLimit
				raw, err = f.db.ReadDiagnostics(ctx, "controller", f.token, f.request)
			}
			if err != nil {
				t.Fatal(err)
			}
			var control, after int64
			if err = f.db.sql.QueryRow(`SELECT pending_control_bytes,(SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id='') FROM diagnostic_delivery_state`).Scan(&control, &after); err != nil || control != int64(len(raw)-base64.StdEncoding.EncodedLen(diagnostics.RecordBytes)) || after-before > 8192 {
				t.Fatal("raw payload entered control quota", control, after-before, err)
			}
		})
	}
}

func TestDiagnosticsPublicEscapedMetadataPaginatesInsideWholeIPC(t *testing.T) {
	f := newDiagnosticsReadFixture(t)
	ctx := context.Background()
	for i := 0; i < 64; i++ {
		rec := diagnostics.Record{SegmentID: fmt.Sprintf("%03d", i) + strings.Repeat("\x01", 125), Stream: diagnostics.Stdout, Data: bytes.Repeat([]byte{byte(i)}, 512)}
		if err := f.writer.Append(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.writer.Close(ctx); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for pages := 0; pages < 8 && seen < 64; pages++ {
		raw, err := f.db.ReadDiagnostics(ctx, "controller", f.token, f.request)
		var page DiagnosticsEnvelope
		if err != nil || json.Unmarshal(raw, &page) != nil || page.Body.Status != "available" || len(page.Body.Records) == 0 || len(raw) > DiagnosticsEnvelopeBytes {
			t.Fatal("escaped page did not make bounded progress", len(raw), err)
		}
		if pages == 0 && len(page.Body.Records) == 64 {
			t.Fatal("fixture did not exercise metadata pagination")
		}
		wire, err := json.Marshal(ipc.Envelope{Version: 1, Kind: ipc.KindResponse, RequestID: strings.Repeat("x", 128), Epoch: math.MaxUint64, Payload: json.RawMessage(raw)})
		if err != nil || len(wire) > ipc.MaxMessageSize {
			t.Fatal("persisted page cannot fit complete IPC", len(wire), err)
		}
		for _, rec := range page.Body.Records {
			if rec.SegmentID != fmt.Sprintf("%03d", seen)+strings.Repeat("\x01", 125) || !bytes.Equal(rec.Data, bytes.Repeat([]byte{byte(seen)}, 512)) {
				t.Fatal("pagination skipped or repeated a record", seen)
			}
			seen++
		}
		ack, err := f.db.AckDiagnostics(ctx, "controller", f.token, page.Receipt)
		if err != nil {
			t.Fatal(err)
		}
		f.request.ExpectedRevision, f.request.RequestID, f.request.Cursor = ack.NextRevision, fmt.Sprint("page-", pages+2), &page.Receipt.Next
	}
	if seen != 64 {
		t.Fatal("bounded pagination did not reach all records", seen)
	}
}

func TestDiagnosticsReadRequestRejectsAmbiguousJSON(t *testing.T) {
	base := `{"version":1,"task_id":"task","attempt_id":"attempt","request_id":"read","expected_revision":1}`
	for _, raw := range []string{
		strings.Replace(base, `"version":1`, `"version":1,"Version":1`, 1),
		strings.Replace(base, `"request_id":"read"`, `"request_id":"read","request_id":"other"`, 1),
		strings.Replace(base, `"expected_revision":1`, `"expected_revision":null`, 1),
		strings.TrimSuffix(base, "}") + `,"cursor":null}`,
		strings.TrimSuffix(base, "}") + `,"root":"/private/foreign"}`,
		base + ` {}`,
	} {
		var req DiagnosticsReadRequest
		if json.Unmarshal([]byte(raw), &req) == nil {
			t.Fatal("ambiguous diagnostics request accepted", raw)
		}
	}
}

func TestDiagnosticsACKBindsEveryReceiptFieldAndAuthority(t *testing.T) {
	f := newDiagnosticsReadFixture(t)
	f.append(t, 0, []byte("private"))
	ctx := context.Background()
	raw, err := f.db.ReadDiagnostics(ctx, "controller", f.token, f.request)
	var page DiagnosticsEnvelope
	if err != nil || json.Unmarshal(raw, &page) != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"token", "controller", "version", "run", "task", "attempt", "producer", "reservation", "policy", "revision", "request", "delivery", "hash", "start", "next", "gap"} {
		t.Run(kind, func(t *testing.T) {
			r := page.Receipt
			controller, token := "controller", f.token
			switch kind {
			case "token":
				token = "wrong"
			case "controller":
				controller = "wrong"
			case "version":
				r.Identity.Version = 2
			case "run":
				r.Identity.RunID = "foreign"
			case "task":
				r.Identity.TaskID = "foreign"
			case "attempt":
				r.Identity.AttemptID = "foreign"
			case "producer":
				r.Identity.ProducerID = "foreign"
			case "reservation":
				r.Identity.ReservationID = "foreign"
			case "policy":
				r.Identity.PolicySHA256 = strings.Repeat("0", 64)
			case "revision":
				r.Identity.Revision++
			case "request":
				r.Identity.RequestID = "foreign"
			case "delivery":
				r.Identity.DeliveryID = "foreign"
			case "hash":
				r.BodySHA256 = strings.Repeat("0", 64)
			case "start":
				r.Start.ChunkSequence++
			case "next":
				r.Next.DataOffset++
			case "gap":
				r.Gap = &diagnostics.PageGap{From: r.Start, To: r.Next}
			}
			if _, err := f.db.AckDiagnostics(ctx, controller, token, r); err == nil {
				t.Fatal("modified ACK accepted")
			}
			var saved []byte
			if err := f.db.sql.QueryRow(`SELECT pending_envelope FROM diagnostic_delivery_state WHERE phase='pending'`).Scan(&saved); err != nil || !bytes.Equal(raw, saved) {
				t.Fatal("rejected ACK changed pending receipt", err)
			}
		})
	}
}

func TestDiagnosticsPublicRollingGapAndPendingRemainExact(t *testing.T) {
	f := newDiagnosticsReadFixture(t)
	ctx := context.Background()
	data := bytes.Repeat([]byte("r"), diagnostics.RecordBytes)
	for i := 0; i < (diagnostics.SlotCount+2)*2; i++ {
		if err := f.writer.Append(ctx, diagnostics.Record{SegmentID: "rolling", Stream: diagnostics.Stdout, Offset: uint64(i * len(data)), Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.writer.Close(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := f.db.ReadDiagnostics(ctx, "controller", f.token, f.request)
	var page DiagnosticsEnvelope
	if err != nil || json.Unmarshal(raw, &page) != nil || page.Body.Status != "available" || page.Body.Gap == nil || page.Receipt.Gap == nil || page.Body.Snapshot == nil || !page.Body.Snapshot.Truncated || page.Body.Gap.From != page.Body.Start || page.Body.Gap.To.ChunkSequence <= 1 {
		t.Fatal("public rolling loss was not explicit in full receipt", err)
	}
	changed := page.Receipt
	gap := *changed.Gap
	gap.To.ChunkSequence++
	changed.Gap = &gap
	if _, err = f.db.AckDiagnostics(ctx, "controller", f.token, changed); err == nil {
		t.Fatal("ACK discarded/changed rolling gap")
	}
	again, err := f.db.ReadDiagnostics(ctx, "controller", f.token, f.request)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatal("gap replay changed", err)
	}
	if _, err = f.db.AckDiagnostics(ctx, "controller", f.token, page.Receipt); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticsPublicCancellationDoesNotWaitForDatabaseMutex(t *testing.T) {
	for _, phase := range []string{"begin", "finish", "ack"} {
		t.Run(phase, func(t *testing.T) {
			f := newDiagnosticsReadFixture(t)
			var receipt DiagnosticsReceipt
			if phase == "ack" {
				raw, err := f.db.ReadDiagnostics(context.Background(), "controller", f.token, f.request)
				var page DiagnosticsEnvelope
				if err != nil || json.Unmarshal(raw, &page) != nil {
					t.Fatal(err)
				}
				receipt = page.Receipt
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			locked := make(chan struct{})
			result := make(chan error, 1)
			if phase != "finish" {
				f.db.mu.Lock()
				cancel()
				close(locked)
			}
			go func() {
				var err error
				if phase == "ack" {
					_, err = f.db.AckDiagnostics(ctx, "controller", f.token, receipt)
				} else if phase == "begin" {
					_, err = f.db.ReadDiagnostics(ctx, "controller", f.token, f.request)
				} else {
					_, err = f.db.readDiagnostics(ctx, "controller", f.token, f.request, func(ctx context.Context, fence diagnosticReadFence) ([]byte, error) {
						page, err := loadDiagnosticEnvelope(ctx, fence)
						if err != nil {
							return nil, err
						}
						f.db.mu.Lock()
						cancel()
						close(locked)
						return page, nil
					})
				}
				result <- err
			}()
			select {
			case <-locked:
			case err := <-result:
				select {
				case <-locked:
					result <- err
				default:
					t.Fatal("fixture did not reach held mutex", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("fixture failed to enter")
			}
			var unlock sync.Once
			release := func() { unlock.Do(f.db.mu.Unlock) }
			defer release()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cancelled request got authority after cancellation", err)
				}
			case <-time.After(time.Second):
				t.Error("cancelled diagnostics request waited for unrelated database mutex")
				release()
				select {
				case <-result:
				case <-time.After(3 * time.Second):
					t.Fatal("original request did not join after release")
				}
			}
		})
	}
}
