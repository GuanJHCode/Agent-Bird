package host

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

const identityA = "11111111-1111-1111-1111-111111111111"
const identityB = "22222222-2222-2222-2222-222222222222"
const identityC = "33333333-3333-3333-3333-333333333333"

type identitySummaryView struct {
	ByteLen int    `json:"byte_len"`
	SHA256  string `json:"sha256"`
	UUID    string `json:"uuid"`
}
type identityObservationView struct {
	Phase     string              `json:"phase"`
	LineIndex int64               `json:"line_index"`
	Observed  identitySummaryView `json:"observed"`
	Status    string              `json:"status"`
	Reason    string              `json:"reason"`
}
type identityDiagnosticsView struct {
	Expected      identitySummaryView      `json:"expected"`
	FirstInit     *identityObservationView `json:"first_init"`
	FirstTerminal *identityObservationView `json:"first_terminal"`
	FirstMismatch *identityObservationView `json:"first_mismatch"`
}

func identityCollector(t *testing.T, expected string) *protocolCollector {
	t.Helper()
	p, err := newProtocolCollector("antigravity-cli")
	if err != nil {
		t.Fatal(err)
	}
	p.requireSession = true
	p.expectedSessionID = expected
	return p
}
func identityFrame(phase, id, status string) string {
	var value any
	switch phase {
	case "init":
		value = map[string]any{"event": "init", "conversation_id": id}
	case "step_update":
		value = map[string]any{"event": "step_update", "step_update": map[string]any{"conversation_id": id, "text_delta": "MODEL_BODY_SENTINEL"}}
	case "result":
		value = map[string]any{"event": "result", "result": map[string]any{"conversation_id": id, "status": status, "response": "MODEL_BODY_SENTINEL", "structured_output": map[string]any{"version": 1, "kind": "result", "text": "MODEL_STRUCTURED_SENTINEL"}}}
	default:
		panic("test phase")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(raw) + "\n"
}
func writeIdentityFrame(t *testing.T, p *protocolCollector, frame string) {
	t.Helper()
	// Exercise the real incremental parser, including tokens split across Writes.
	split := len(frame) / 2
	for _, part := range []string{frame[:split], frame[split:]} {
		n, err := p.Write([]byte(part))
		if err != nil || n != len(part) {
			t.Fatalf("Write: %d %v", n, err)
		}
	}
}
func identityView(t *testing.T, snapshot protocolSnapshot) *identityDiagnosticsView {
	t.Helper()
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Identity *identityDiagnosticsView `json:"session_identity"`
	}
	if err = json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	if view.Identity == nil {
		t.Fatal("parsed native session identity was not retained in diagnostic snapshot")
	}
	return view.Identity
}
func identityDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func assertIdentitySummary(t *testing.T, got identitySummaryView, value, disclosed string) {
	t.Helper()
	if got.ByteLen != len(value) || got.SHA256 != identityDigest(value) || got.UUID != disclosed {
		t.Fatalf("wrong identity summary: %+v", got)
	}
}

func TestProtocolIdentityRecordsSessionDriftBeforeRejection(t *testing.T) {
	for _, phase := range []string{"init", "result", "step_update"} {
		t.Run(phase, func(t *testing.T) {
			p := identityCollector(t, identityA)
			calls := 0
			if err := p.SetSessionObserver(func(kind, id string) error {
				calls++
				if kind != "conversation-id" || id != identityA {
					t.Error("diagnostics changed observer identity")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			wantCalls, wantLine := 0, int64(1)
			if phase != "init" {
				writeIdentityFrame(t, p, identityFrame("init", identityA, ""))
				wantCalls, wantLine = 1, 2
			}
			writeIdentityFrame(t, p, identityFrame(phase, identityB, "SUCCESS"))
			snapshot := p.Snapshot()
			if snapshot.ProtocolError != "provider_session_changed" || snapshot.TerminalSeen || calls != wantCalls {
				t.Fatalf("original rejection changed: %+v calls=%d", snapshot, calls)
			}
			trace := identityView(t, snapshot)
			assertIdentitySummary(t, trace.Expected, identityA, "")
			if trace.FirstMismatch == nil || trace.FirstMismatch.Phase != phase || trace.FirstMismatch.Reason != "expected_drift" || trace.FirstMismatch.LineIndex != wantLine {
				t.Fatalf("missing pre-rejection observation: %+v", trace.FirstMismatch)
			}
			assertIdentitySummary(t, trace.FirstMismatch.Observed, identityB, identityB)
			if phase == "init" && (trace.FirstInit == nil || trace.FirstTerminal != nil || p.sessionID != "") {
				t.Fatal("init rejection became a Session or terminal")
			}
			if phase == "result" && (trace.FirstTerminal == nil || trace.FirstTerminal.Status != "SUCCESS") {
				t.Fatal("rejected incoming result identity disappeared")
			}
			if phase != "init" && p.sessionID != identityA {
				t.Fatal("rejected identity replaced original Session")
			}
			_, _, err := p.Finish()
			if err == nil || err.Error() != "provider_session_changed" || calls != wantCalls {
				t.Fatal("diagnostics changed Finish error or observer")
			}
		})
	}
}

func TestProtocolIdentityPreservesAcceptedNativeResults(t *testing.T) {
	for _, status := range []string{"SUCCESS", "WAITING", "ERROR"} {
		t.Run(status, func(t *testing.T) {
			p := identityCollector(t, identityA)
			calls := 0
			if err := p.SetSessionObserver(func(string, string) error { calls++; return nil }); err != nil {
				t.Fatal(err)
			}
			writeIdentityFrame(t, p, identityFrame("init", identityA, ""))
			writeIdentityFrame(t, p, identityFrame("result", identityA, status))
			snapshot := p.Snapshot()
			trace := identityView(t, snapshot)
			if !snapshot.TerminalSeen || snapshot.ProtocolError != "none" || trace.FirstMismatch != nil || trace.FirstInit == nil || trace.FirstTerminal == nil || trace.FirstTerminal.Status != status {
				t.Fatal("diagnostics changed accepted native terminal")
			}
			assertIdentitySummary(t, trace.FirstTerminal.Observed, identityA, identityA)
			result, session, err := p.Finish()
			if err != nil || result.Status != status || result.Text != "MODEL_BODY_SENTINEL" || session != identityA || calls != 1 {
				t.Fatalf("original result changed: status=%s session=%s calls=%d err=%v", result.Status, session, calls, err)
			}
		})
	}
}

func TestProtocolIdentityWithinSegmentDriftAndFirstSlots(t *testing.T) {
	p := identityCollector(t, "")
	writeIdentityFrame(t, p, identityFrame("init", identityA, ""))
	// A long valid stream must not allocate a diagnostic event queue.
	for i := 0; i < 2000; i++ {
		writeIdentityFrame(t, p, identityFrame("step_update", identityA, ""))
	}
	writeIdentityFrame(t, p, identityFrame("result", identityB, "SUCCESS"))
	first := identityView(t, p.Snapshot())
	if first.FirstMismatch == nil || first.FirstMismatch.Reason != "within_segment_drift" || first.FirstMismatch.LineIndex != 2002 {
		t.Fatal("within-segment guard observation missing")
	}
	assertIdentitySummary(t, first.FirstInit.Observed, identityA, identityA)
	assertIdentitySummary(t, first.FirstTerminal.Observed, identityB, identityB)
	// The original fatal-error path does not parse later frames. Do not invent them.
	writeIdentityFrame(t, p, identityFrame("init", identityC, ""))
	writeIdentityFrame(t, p, identityFrame("result", identityC, "ERROR"))
	later := identityView(t, p.Snapshot())
	before, _ := json.Marshal(first)
	after, _ := json.Marshal(later)
	if string(before) != string(after) || p.sessionID != identityA || p.terminal != nil {
		t.Fatal("later raw input replaced first observation or business state")
	}
	raw, _ := json.Marshal(later)
	if len(raw) > 2048 {
		t.Fatal("fixed diagnostic slots exceeded 2KiB")
	}
}

func TestProtocolIdentitySnapshotIsDetachedValue(t *testing.T) {
	p := identityCollector(t, identityA)
	writeIdentityFrame(t, p, identityFrame("init", identityA, ""))
	saved := p.Snapshot()
	before := identityView(t, saved)
	// A consumer mutates the returned value through actual JSON decoding. A
	// shallow copy would mutate the collector's retained observation as well.
	mutation := fmt.Sprintf(`{"session_identity":{"first_init":{"observed":{"byte_len":36,"sha256":%q,"uuid":%q}}}}`, identityDigest(identityB), identityB)
	if err := json.Unmarshal([]byte(mutation), &saved); err != nil {
		t.Fatal(err)
	}
	current := identityView(t, p.Snapshot())
	assertIdentitySummary(t, current.FirstInit.Observed, identityA, identityA)
	writeIdentityFrame(t, p, identityFrame("result", identityA, "SUCCESS"))
	if before.FirstTerminal != nil || current.FirstTerminal != nil {
		t.Fatal("future Write changed an earlier snapshot")
	}
}

func TestProtocolIdentityPrivacyAndFullDiagnosticJSON(t *testing.T) {
	for _, id := range []string{"PRIVATE_ENV_TOKEN_SENTINEL", "ABCDEFAB-CDEF-ABCD-EFAB-CDEFABCDEFAB", strings.Repeat("PRIVATE_ID_SENTINEL", 1500)} {
		t.Run(fmt.Sprintf("bytes_%d", len(id)), func(t *testing.T) {
			p := identityCollector(t, id)
			writeIdentityFrame(t, p, identityFrame("init", id, ""))
			writeIdentityFrame(t, p, identityFrame("result", id, "SUCCESS"))
			snapshot := p.Snapshot()
			trace := identityView(t, snapshot)
			assertIdentitySummary(t, trace.Expected, id, "")
			assertIdentitySummary(t, trace.FirstInit.Observed, id, "")
			rawInput := identityFrame("init", id, "") + identityFrame("result", id, "SUCCESS") + `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":"PRIVATE_TOOL_ARGUMENT_SENTINEL"}]},"environment":"PRIVATE_ENVIRONMENT_SENTINEL"}` + "\n"
			report := providerDiagnostics(rawInput)
			copy := p.Snapshot()
			report.ProtocolSnapshots = &protocolDiagnostics{AfterWait: snapshot, AfterStop: &copy}
			body, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			for _, sentinel := range []string{id, "MODEL_BODY_SENTINEL", "MODEL_STRUCTURED_SENTINEL", "PRIVATE_TOOL_ARGUMENT_SENTINEL", "PRIVATE_ENVIRONMENT_SENTINEL"} {
				if strings.Contains(string(body), sentinel) {
					t.Fatal("full diagnostics exposed raw identity, body, tool arguments or environment")
				}
			}
			added, _ := json.Marshal(trace)
			pair, _ := json.Marshal(report.ProtocolSnapshots)
			if len(added) > 2048 || len(pair) > 4096 {
				t.Fatalf("diagnostic bounds: %d/%d", len(added), len(pair))
			}
			var restored struct {
				Protocol struct {
					AfterWait struct {
						Identity *identityDiagnosticsView `json:"session_identity"`
					} `json:"after_wait"`
				} `json:"protocol_snapshots"`
			}
			if json.Unmarshal(body, &restored) != nil || restored.Protocol.AfterWait.Identity == nil {
				t.Fatal("real report serialization dropped native identity observations")
			}
		})
	}
}

func TestProtocolIdentityParserRejectionDoesNotRecoverRawFields(t *testing.T) {
	for name, frame := range map[string]string{
		"numeric_id":     `{"event":"init","conversation_id":123}` + "\n",
		"invalid_status": fmt.Sprintf(`{"event":"result","result":{"conversation_id":%q,"status":"PRIVATE_STATUS_SENTINEL","response":"PRIVATE_RESPONSE_SENTINEL"}}`, identityB) + "\n",
		"trailing_json":  identityFrame("init", identityB, "")[:len(identityFrame("init", identityB, ""))-1] + ` {"PRIVATE_TRAILING_SENTINEL":true}` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := identityCollector(t, identityA)
			writeIdentityFrame(t, p, frame)
			snapshot := p.Snapshot()
			if snapshot.ProtocolError == "none" || snapshot.TerminalSeen || p.sessionID != "" {
				t.Fatal("parser rejection was changed")
			}
			trace := identityView(t, snapshot)
			if trace.FirstInit != nil || trace.FirstTerminal != nil || trace.FirstMismatch != nil {
				t.Fatal("diagnostics recovered identity from parser-rejected raw fields")
			}
			writeIdentityFrame(t, p, identityFrame("init", identityB, ""))
			after := identityView(t, p.Snapshot())
			if after.FirstInit != nil {
				t.Fatal("fatal parser path was reopened for diagnostics")
			}
			body, _ := json.Marshal(snapshot)
			if strings.Contains(string(body), identityB) || strings.Contains(string(body), "PRIVATE_") {
				t.Fatal("parser-rejected raw fields leaked")
			}
		})
	}
}

func TestProtocolIdentitySnapshotDoesNotWaitForObserver(t *testing.T) {
	p := identityCollector(t, identityA)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	if err := p.SetSessionObserver(func(string, string) error { close(entered); <-release; return nil }); err != nil {
		t.Fatal(err)
	}
	go func() { defer close(done); _, _ = p.Write([]byte(identityFrame("init", identityA, ""))) }()
	<-entered
	snapshotDone := make(chan struct{})
	values := make(chan protocolSnapshot, 1)
	go func() { defer close(snapshotDone); values <- p.Snapshot() }()
	defer func() { close(release); <-done; <-snapshotDone }()
	select {
	case snapshot := <-values:
		if snapshot.Available {
			t.Fatal("contended snapshot claimed an observation")
		}
		body, _ := json.Marshal(snapshot)
		if strings.Contains(string(body), "session_identity") || strings.Contains(string(body), identityA) {
			t.Fatal("contended snapshot accessed unavailable identity")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("diagnostic identity blocked behind the original observer")
	}
}

func TestProtocolIdentityConcurrentSnapshotsPreserveOriginalResult(t *testing.T) {
	p := identityCollector(t, identityA)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = p.Write([]byte(identityFrame("init", identityA, "")))
		for i := 0; i < 1000; i++ {
			_, _ = p.Write([]byte(identityFrame("step_update", identityA, "")))
		}
		_, _ = p.Write([]byte(identityFrame("result", identityA, "SUCCESS")))
	}()
	for i := 0; i < 1000; i++ {
		snapshot := p.Snapshot()
		if snapshot.Available {
			body, err := json.Marshal(snapshot)
			if err != nil || len(body) > 2304 {
				t.Error("concurrent snapshot failed bounded serialization")
			}
		}
	}
	<-done
	trace := identityView(t, p.Snapshot())
	if trace.FirstInit == nil || trace.FirstTerminal == nil || trace.FirstMismatch != nil {
		t.Fatal("concurrent identity observation lost accepted result")
	}
	result, session, err := p.Finish()
	if err != nil || session != identityA || result.Status != "SUCCESS" {
		t.Fatal("concurrent snapshots changed original Finish")
	}
}
