package store

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/diagnostics"
)

type diagnosticsReadFixture struct {
	db      *DB
	grant   contract.LaunchCommand
	hello   contract.HostHello
	token   string
	root    string
	writer  *diagnostics.Writer
	request DiagnosticsReadRequest
}

func newDiagnosticsReadFixture(t *testing.T) *diagnosticsReadFixture {
	t.Helper()
	ctx := context.Background()
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	f := &diagnosticsReadFixture{db: db}
	t.Cleanup(func() { _ = f.db.Close() })
	r, err := db.SubmitPlan(ctx, diagnosticsPlan(&contract.DiagnosticsPolicy{Version: 1, Mode: "detailed"}))
	if err != nil {
		t.Fatal(err)
	}
	f.token = r.ControlToken
	h := contract.HostHello{LaunchID: r.LaunchID, LaunchToken: r.LaunchToken, OriginContextID: "origin", OriginPID: 7, OriginBirth: "birth", HostGeneration: "generation", PID: 101, Birth: "host-birth", Executable: "/private/tmp/orchestrator"}
	f.hello = h
	if _, err = db.RegisterHost(ctx, h, 1); err != nil {
		t.Fatal(err)
	}
	f.grant, err = db.ClaimReady(ctx, h.LaunchID, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	f.root = filepath.Join(state, "host-spool", f.grant.HostID, f.grant.AttemptID, ".diagnostics")
	if err = os.MkdirAll(f.root, 0700); err != nil {
		t.Fatal(err)
	}
	scope := diagnostics.Scope{Version: 1, RunID: f.grant.RunID, TaskID: f.grant.TaskID, AttemptID: f.grant.AttemptID, ProducerID: f.grant.HostID, PolicySHA256: f.grant.Diagnostics.PolicySHA256}
	f.writer, err = diagnostics.OpenWriter(ctx, f.root, scope)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.writer.Close(context.Background()) })
	f.request = DiagnosticsReadRequest{Version: 1, TaskID: "task", AttemptID: f.grant.AttemptID, RequestID: "read-1", ExpectedRevision: 1}
	return f
}

func (f *diagnosticsReadFixture) append(t *testing.T, offset uint64, data []byte) {
	t.Helper()
	if err := f.writer.Append(context.Background(), diagnostics.Record{SegmentID: "segment", Stream: diagnostics.Stdout, Offset: offset, Data: data}); err != nil {
		t.Fatal(err)
	}
	if err := f.writer.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func diagnosticsBusinessSnapshot(t *testing.T, db *DB) string {
	t.Helper()
	var tables []any
	for _, table := range []string{"tasks", "attempts", "segments", "segment_runtime", "budget_runtime", "managed_artifact_reservations", "runtime_events", "runtime_decisions"} {
		rows, err := db.sql.Query("SELECT * FROM " + table + " ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		var records [][]any
		for rows.Next() {
			values, slots := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				slots[i] = &values[i]
			}
			if err = rows.Scan(slots...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			records = append(records, values)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		tables = append(tables, records)
	}
	raw, _ := json.Marshal(tables)
	return string(raw)
}

func TestDiagnosticsPublicReadPersistsReplayAndACKWithoutBusinessMutation(t *testing.T) {
	f := newDiagnosticsReadFixture(t)
	ctx := context.Background()
	f.append(t, 0, bytes.Repeat([]byte("x"), diagnostics.RecordBytes))
	f.append(t, diagnostics.RecordBytes, []byte("tail"))
	if err := f.writer.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// Data authority survives execution unavailability; no native process is
	// asserted by this Store fixture.
	if _, err := f.db.sql.Exec(`UPDATE runtime_hosts SET status='offline'`); err != nil {
		t.Fatal(err)
	}
	before := diagnosticsBusinessSnapshot(t, f.db)
	raw, err := f.db.ReadDiagnostics(ctx, "controller", f.token, f.request)
	if err != nil {
		t.Fatal(err)
	}
	var page DiagnosticsEnvelope
	if err = json.Unmarshal(raw, &page); err != nil || len(page.Body.Records) != 1 || len(page.Body.Records[0].Data) != diagnostics.RecordBytes || page.Body.Status != "available" || page.Receipt.Identity.Revision != 1 || page.Receipt.Start.ScopeSHA256 == "" || len(raw) > DiagnosticsEnvelopeBytes {
		t.Fatalf("first public page invalid: bytes=%d records=%d err=%v", len(raw), len(page.Body.Records), err)
	}
	path := f.db.path
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(f.root, f.root+"-hidden"); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Rename(f.root+"-hidden", f.root) }()
	again, err := f.db.ReadDiagnostics(ctx, "controller", f.token, f.request)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatal("pending replay did filesystem work or changed bytes", err)
	}
	wrong := page.Receipt
	wrong.Next.RecordOffset++
	if _, err = f.db.AckDiagnostics(ctx, "controller", f.token, wrong); err == nil {
		t.Fatal("ACK did not bind complete cursor receipt")
	}
	ack, err := f.db.AckDiagnostics(ctx, "controller", f.token, page.Receipt)
	if err != nil || ack.NextRevision != 2 {
		t.Fatal("exact ACK failed", err)
	}
	duplicate, err := f.db.AckDiagnostics(ctx, "controller", f.token, page.Receipt)
	if err != nil || !reflect.DeepEqual(ack, duplicate) {
		t.Fatal("lost ACK reply not exactly replayable", err)
	}
	if _, err = f.db.ReadDiagnostics(ctx, "controller", f.token, f.request); err == nil {
		t.Fatal("stale read got a new delivery")
	}
	if err = os.Rename(f.root+"-hidden", f.root); err != nil {
		t.Fatal(err)
	}
	acknowledgedCursor := page.Receipt.Next
	f.request.RequestID, f.request.ExpectedRevision, f.request.Cursor = "read-2", 2, &acknowledgedCursor
	next, err := f.db.ReadDiagnostics(ctx, "controller", f.token, f.request)
	if err != nil || json.Unmarshal(next, &page) != nil || len(page.Body.Records) != 1 || string(page.Body.Records[0].Data) != "tail" {
		t.Fatal("ACK cursor lost the tail", err)
	}
	if duplicate, err = f.db.AckDiagnostics(ctx, "controller", f.token, ack.Receipt); err != nil || !reflect.DeepEqual(ack, duplicate) {
		t.Fatal("latest duplicate ACK failed while a newer page was pending", err)
	}
	if replay, err := f.db.ReadDiagnostics(ctx, "controller", f.token, f.request); err != nil || !bytes.Equal(next, replay) {
		t.Fatal("old duplicate ACK erased the newer pending page", err)
	}
	if _, err = f.db.AckDiagnostics(ctx, "controller", f.token, page.Receipt); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.AckDiagnostics(ctx, "controller", f.token, ack.Receipt); err == nil {
		t.Fatal("older ACK remained current")
	}
	if after := diagnosticsBusinessSnapshot(t, f.db); after != before {
		t.Fatal("diagnostic read/ACK mutated execution, business or reservation accounting")
	}
}

func TestDiagnosticsPublicReadRejectsForeignBeforeNamespaceAccess(t *testing.T) {
	f := newDiagnosticsReadFixture(t)
	ctx := context.Background()
	before := diagnosticsBusinessSnapshot(t, f.db)
	for _, kind := range []string{"token", "controller", "attempt", "initial_cursor", "revision", "path"} {
		t.Run(kind, func(t *testing.T) {
			req, controller, token := f.request, "controller", f.token
			switch kind {
			case "token":
				token = "wrong"
			case "controller":
				controller = "foreign"
			case "attempt":
				req.AttemptID = "foreign"
			case "revision":
				req.ExpectedRevision = 2
			case "path":
				req.AttemptID = "../foreign"
			case "initial_cursor":
				req.Cursor = &diagnostics.PageCursor{ScopeSHA256: strings.Repeat("a", 64)}
			}
			if _, err := f.db.ReadDiagnostics(ctx, controller, token, req); err == nil {
				t.Fatal("foreign/stale request accepted")
			}
			var phase string
			var revision int64
			if err := f.db.sql.QueryRow(`SELECT phase,next_revision FROM diagnostic_delivery_state`).Scan(&phase, &revision); err != nil || phase != "idle" || revision != 1 {
				t.Fatal("rejected request created a reading fence", phase, revision, err)
			}
		})
	}
	if before != diagnosticsBusinessSnapshot(t, f.db) {
		t.Fatal("rejected request mutated business state")
	}
}
