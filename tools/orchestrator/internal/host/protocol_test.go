package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
)

// This fixture models the currently pinned codex-cli 0.154.0 JSONL schema;
// it is synthetic and does not establish live provider compatibility.
func TestCodex01540ProtocolAcrossEveryTwoPartSplit(t *testing.T) {
	data, err := os.ReadFile("../adapter/testdata/codex-success.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for split := 0; split <= len(data); split++ {
		collector, err := newProtocolCollector("codex-cli")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = collector.Write(data[:split])
		_, _ = collector.Write(data[split:])
		terminal, session, err := collector.Finish()
		if err != nil || terminal.Text != "bounded answer" || session != "codex-thread-1" || terminal.Status != "completed" {
			t.Fatalf("split=%d terminal=%#v session=%q err=%v", split, terminal, session, err)
		}
	}
}

func TestCodexOversizeDiagnosticPolicyAndEOF(t *testing.T) {
	for _, tc := range []struct{ name, line, want string }{
		{"raw diagnostic", "diagnostic: " + strings.Repeat("x", providerEventLimit), "provider_terminal_missing"},
		{"ambiguous JSON diagnostic", `{"type":"future.diagnostic","text":"` + strings.Repeat("x", providerEventLimit) + `"}`, "provider_critical_event_too_large"},
		{"JSON after long whitespace", strings.Repeat(" ", providerEventLimit+1) + `{"type":"turn.failed","error":{"message":"failure"}}`, "provider_critical_event_too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			collector, err := newProtocolCollector("codex-cli")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = collector.Write([]byte(tc.line))
			if _, _, err := collector.Finish(); err == nil || err.Error() != tc.want {
				t.Fatalf("err=%v want=%s", err, tc.want)
			}
			if tc.name == "raw diagnostic" && collector.droppedOversize != 1 {
				t.Fatal("raw diagnostic not drained")
			}
		})
	}
}

func TestProtocolLateObserverSeparatesOutputFailureFromPersistenceFailure(t *testing.T) {
	for _, failObserver := range []bool{false, true} {
		t.Run(fmt.Sprint(failObserver), func(t *testing.T) {
			collector, err := newProtocolCollector("codex-cli")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = collector.Write([]byte(`{"type":"thread.started","thread_id":"s"}` + "\n" +
				`{"type":"item.completed","item":{"id":"final","type":"agent_message","text":"` + strings.Repeat("x", providerEventLimit) + `"}}` + "\n"))
			calls := 0
			observerFailure := errors.New("test_persistence_failed")
			err = collector.SetSessionObserver(func(kind, id string) error {
				calls++
				if kind != "session-id" || id != "s" {
					t.Fatalf("session=%s/%s", kind, id)
				}
				if failObserver {
					return observerFailure
				}
				return nil
			})
			if calls != 1 || (failObserver && !errors.Is(err, observerFailure)) || (!failObserver && err != nil) {
				t.Fatalf("observer calls=%d err=%v", calls, err)
			}
			if _, _, err := collector.Finish(); err == nil {
				t.Fatal("output failure lost")
			}
		})
	}
}

func TestCodexOversizedMessageNeverFallsBackToEarlierAnswer(t *testing.T) {
	large := strings.Repeat("x", providerEventLimit+1)
	for _, line := range []string{
		`{"type":"item.completed","item":{"id":"final","type":"agent_message","text":"` + large + `"}}`,
		`{"item":{"text":"` + large + `","id":"final","type":"agent_message"},"type":"item.completed"}`,
		`{"type":"item.completed","item":{"id":"final","type":"agent_\u006dessage","text":"` + large + `"}}`,
	} {
		for _, chunk := range []int{1, 127, 4096, 65536} {
			t.Run(fmt.Sprintf("shape-%d/chunk-%d", strings.Index(line, large), chunk), func(t *testing.T) {
				collector, err := newProtocolCollector("codex-cli")
				if err != nil {
					t.Fatal(err)
				}
				stream := `{"type":"thread.started","thread_id":"s"}` + "\n" +
					`{"type":"item.completed","item":{"id":"draft","type":"agent_message","text":"old draft"}}` + "\n" + line + "\n" +
					`{"type":"turn.completed","usage":{"input_tokens":7,"cached_input_tokens":3,"cache_write_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":1}}`
				for len(stream) > 0 {
					n := min(chunk, len(stream))
					if _, err := collector.Write([]byte(stream[:n])); err != nil {
						t.Fatal(err)
					}
					stream = stream[n:]
				}
				terminal, session, err := collector.Finish()
				if err == nil || err.Error() != "provider_critical_event_too_large" || terminal.Text != "" || session != "s" {
					t.Fatalf("terminal=%#v session=%q err=%v; oversized final must be incomplete", terminal, session, err)
				}
			})
		}
	}
}

func TestCodexProtocolRequiresOneSessionBeforeOneTerminal(t *testing.T) {
	collector, err := newProtocolCollector(string(adapter.ProviderCodex))
	if err != nil {
		t.Fatal(err)
	}
	var sessionKind, sessionID string
	if err := collector.SetSessionObserver(func(kind, id string) error {
		sessionKind, sessionID = kind, id
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"codex-thread-1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item-1","type":"reasoning","text":"discard"}}`,
		`{"type":"item.completed","item":{"id":"item-2","type":"agent_message","text":"intermediate commentary"}}`,
		`{"type":"error","message":"retrying transport"}`,
		`{"type":"item.completed","item":{"id":"item-3","type":"agent_message","text":"final answer"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":7,"cached_input_tokens":3,"cache_write_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":1}}`,
	}, "\n") + "\n"
	if _, err := collector.Write([]byte(stream)); err != nil {
		t.Fatal(err)
	}
	terminal, gotSession, err := collector.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if sessionKind != "session-id" || sessionID != "codex-thread-1" || gotSession != sessionID {
		t.Fatalf("observed=%q/%q returned=%q", sessionKind, sessionID, gotSession)
	}
	if terminal.Kind != adapter.EventResult || terminal.Status != "completed" || terminal.Text != "final answer" {
		t.Fatalf("terminal=%#v", terminal)
	}
	if !providerSucceeded(adapter.ProviderCodex, terminal) {
		t.Fatal("completed Codex turn was not successful")
	}
}

func TestCodexProtocolRejectsSequenceAndTerminalViolations(t *testing.T) {
	completed := `{"type":"turn.completed","usage":{"input_tokens":7,"cached_input_tokens":3,"cache_write_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":1}}`
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"terminal before session", []string{completed}, "provider_event_before_session"},
		{"message before session", []string{`{"type":"item.completed","item":{"id":"item-1","type":"agent_message","text":"answer"}}`}, "provider_event_before_session"},
		{"duplicate session", []string{`{"type":"thread.started","thread_id":"s"}`, `{"type":"thread.started","thread_id":"s"}`, completed}, "provider_session_duplicate"},
		{"changed session", []string{`{"type":"thread.started","thread_id":"s"}`, `{"type":"thread.started","thread_id":"other"}`, completed}, "provider_session_changed"},
		{"duplicate terminal", []string{`{"type":"thread.started","thread_id":"s"}`, `{"type":"item.completed","item":{"id":"item-1","type":"agent_message","text":"answer"}}`, completed, `{"type":"turn.failed","error":{"message":"late"}}`}, "provider_multiple_terminal_events"},
		{"message after terminal", []string{`{"type":"thread.started","thread_id":"s"}`, `{"type":"item.completed","item":{"id":"item-1","type":"agent_message","text":"answer"}}`, completed, `{"type":"item.completed","item":{"id":"item-2","type":"agent_message","text":"late"}}`}, "provider_event_after_terminal"},
		{"empty completed", []string{`{"type":"thread.started","thread_id":"s"}`, completed}, "provider_result_missing"},
		{"empty final rejected", []string{`{"type":"thread.started","thread_id":"s"}`, `{"type":"item.completed","item":{"id":"draft","type":"agent_message","text":"old draft"}}`, `{"type":"item.completed","item":{"id":"final","type":"agent_message","text":""}}`, completed}, "event_text_invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			collector, err := newProtocolCollector(string(adapter.ProviderCodex))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := collector.Write([]byte(strings.Join(tc.lines, "\n") + "\n")); err != nil {
				t.Fatal(err)
			}
			if _, _, err := collector.Finish(); err == nil || err.Error() != tc.want {
				t.Fatalf("err=%v want=%q", err, tc.want)
			}
		})
	}
}

func TestProtocolCollectorDrainsRawDiagnosticsUntilStructuredTerminal(t *testing.T) {
	collector, err := newProtocolCollector("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	line := "UNKNOWN:worker:1:" + strings.Repeat("x", 32*1024-len("UNKNOWN:worker:1:")-1) + "\n"
	if _, err = collector.Write([]byte(line + line)); err != nil {
		t.Fatal(err)
	}
	if err = collector.SetSessionObserver(func(string, string) error { return nil }); err != nil {
		t.Fatalf("raw diagnostics poisoned running collector: %v", err)
	}
	if _, err = collector.Write([]byte(`{"type":"result","subtype":"success","session_id":"session-a","result":"ok"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	terminal, session, err := collector.Finish()
	if err != nil || terminal.Text != "ok" || session != "session-a" {
		t.Fatalf("terminal=%#v session=%q err=%v", terminal, session, err)
	}
}

func TestProtocolCollectorDropsOversizedDiagnosticButRejectsOversizedResult(t *testing.T) {
	diagnostic, err := newProtocolCollector("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	largeDiagnostic := `{"type":"diagnostic","message":"` + strings.Repeat("x", 4*1024*1024) + `"}` + "\n"
	if _, err = diagnostic.Write([]byte(largeDiagnostic)); err != nil {
		t.Fatal(err)
	}
	if _, err = diagnostic.Write([]byte(`{"type":"result","subtype":"success","session_id":"session-a","result":"ok"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	terminal, _, err := diagnostic.Finish()
	if err != nil || terminal.Text != "ok" || diagnostic.droppedOversize != 1 {
		t.Fatalf("terminal=%#v dropped=%d err=%v", terminal, diagnostic.droppedOversize, err)
	}

	critical, err := newProtocolCollector("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	largeResult := `{"type":"result","subtype":"success","session_id":"session-a","result":"` + strings.Repeat("x", 4*1024*1024) + `"}` + "\n"
	if _, err = critical.Write([]byte(largeResult)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = critical.Finish(); err == nil || err.Error() != "provider_critical_event_too_large" {
		t.Fatalf("critical err=%v", err)
	}
}

func TestProtocolSnapshotDoesNotParseTailOrInvokeObserver(t *testing.T) {
	collector, err := newProtocolCollector("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err = collector.SetSessionObserver(func(string, string) error {
		calls++
		return errors.New("PRIVATE_SNAPSHOT_SENTINEL /private/credentials")
	}); err != nil {
		t.Fatal(err)
	}
	_, _ = collector.Write([]byte(`{"type":"system","subtype":"init","session_id":"PRIVATE_SNAPSHOT_SENTINEL"}`))
	before := collector.Snapshot()
	if !before.TailPending || before.TerminalSeen || before.ObserverFailed || before.ProtocolError != "none" || calls != 0 {
		t.Fatalf("snapshot changed unresolved tail: %+v calls=%d", before, calls)
	}
	if again := collector.Snapshot(); again != before || calls != 0 {
		t.Fatal("repeated snapshot mutated state")
	}
	_, _ = collector.Write([]byte("\n"))
	after := collector.Snapshot()
	if after.TailPending || !after.ObserverFailed || after.ProtocolError != "other" || calls != 1 {
		t.Fatalf("observer failure shape=%+v calls=%d", after, calls)
	}
	body, err := json.Marshal(after)
	if err != nil || strings.Contains(string(body), "PRIVATE_SNAPSHOT_SENTINEL") || strings.Contains(string(body), "/private") {
		t.Fatal("observer details leaked")
	}
}

func TestProtocolSnapshotOversizeAndTerminalArrival(t *testing.T) {
	collector, _ := newProtocolCollector("claude-code")
	_, _ = collector.Write([]byte(strings.Repeat("diagnostic", providerEventLimit) + "\n"))
	before := collector.Snapshot()
	if before.DroppedOversizeLines != 1 || before.OversizedTail || before.TailPending || before.TerminalSeen {
		t.Fatalf("oversize snapshot=%+v", before)
	}
	_, _ = collector.Write([]byte(`{"type":"result","subtype":"success","session_id":"session","result":"ok"}` + "\n"))
	after := collector.Snapshot()
	if !after.TerminalSeen || before.TerminalSeen {
		t.Fatal("snapshot failed to distinguish later terminal arrival")
	}
	collector, _ = newProtocolCollector("claude-code")
	_, _ = collector.Write([]byte(`{"type":"result","result":"` + strings.Repeat("x", providerEventLimit)))
	tail := collector.Snapshot()
	if !tail.OversizedTail || !tail.TailPending || tail.ProtocolError != "none" {
		t.Fatalf("tail was finalized: %+v", tail)
	}
	_, _ = collector.Write([]byte("\n"))
	if got := collector.Snapshot(); got.ProtocolError != "provider_critical_event_too_large" || got.TailPending {
		t.Fatalf("critical snapshot=%+v", got)
	}
}

func TestProtocolSnapshotConcurrentWriteDoesNotChangeResult(t *testing.T) {
	collector, _ := newProtocolCollector("claude-code")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			_, _ = collector.Write([]byte(`{"type":"system","subtype":"status","session_id":"session"}` + "\n"))
		}
		_, _ = collector.Write([]byte(`{"type":"result","subtype":"success","session_id":"session","result":"ok"}` + "\n"))
	}()
	for i := 0; i < 1000; i++ {
		snapshot := collector.Snapshot()
		if snapshot.ProtocolError != "none" || snapshot.ObserverFailed {
			t.Fatalf("concurrent snapshot=%+v", snapshot)
		}
	}
	<-done
	snapshot := collector.Snapshot()
	if !snapshot.TerminalSeen {
		t.Fatal("terminal missing after writer completes")
	}
	terminal, session, err := collector.Finish()
	if err != nil || session != "session" || terminal.Text != "ok" {
		t.Fatalf("snapshot changed original Finish behavior: %v", err)
	}
}

func TestProtocolSnapshotDoesNotWaitForSessionObserver(t *testing.T) {
	collector, _ := newProtocolCollector("claude-code")
	entered, release, writerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() { close(release); <-writerDone }()
	if err := collector.SetSessionObserver(func(string, string) error { close(entered); <-release; return nil }); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(writerDone)
		_, _ = collector.Write([]byte(`{"type":"system","subtype":"init","session_id":"session"}` + "\n"))
	}()
	<-entered
	snapshots := make(chan any, 1)
	go func() { snapshots <- collector.Snapshot() }()
	select {
	case value := <-snapshots:
		body, _ := json.Marshal(value)
		var got map[string]any
		_ = json.Unmarshal(body, &got)
		available, ok := got["available"].(bool)
		if !ok || available {
			t.Fatal("contended snapshot must explicitly be unavailable")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("diagnostic Snapshot blocked behind observer and would delay deadline Stop")
	}
}
