package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestProviderDiagnosticsDistinguishExactLimitFromDroppedOutput(t *testing.T) {
	line := func(value string) string { return value + strings.Repeat(" ", 255-len(value)) + "\n" }
	output := line(`{"type":"system","subtype":"init","session_id":"capture"}`) +
		strings.Repeat(line(`{"type":"system","subtype":"status"}`), 4094) +
		line(`{"type":"result","subtype":"success","session_id":"capture","result":"ok"}`)
	if len(output) != 1024*1024 {
		t.Fatal("fixture must reach exactly the production capture cap")
	}
	for _, extra := range []bool{false, true} {
		name := "exact_limit"
		if extra {
			name = "one_byte_dropped"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			payload := output
			if extra {
				payload += "\n"
			}
			path := filepath.Join(root, "output.jsonl")
			if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
				t.Fatal(err)
			}
			spoolRoot := filepath.Join(root, "spool")
			h, err := NewIPC(spoolRoot, "producer")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := h.execute(ctx, store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}, "run", "task", process.Command{Path: "/bin/cat", Args: []string{path}}, false, launchMetadata{commandID: "command", workRevision: 1, outputProvider: "claude-code"})
			if err != nil || result.Status != "result_ready" || result.ExitCode != 0 {
				t.Fatalf("controlled stream did not finish: %+v %v", result, err)
			}
			raw, err := os.ReadFile(filepath.Join(spoolRoot, "attempt", "segment", "provider-diagnostics.json"))
			var diagnostics providerDiagnosticReport
			if err != nil || json.Unmarshal(raw, &diagnostics) != nil {
				t.Fatal("missing diagnostics", err)
			}
			if diagnostics.Bytes != len(output) || diagnostics.Truncated != extra || diagnostics.Events["system"] != 4095 || diagnostics.Events["result"] != 1 {
				t.Fatalf("capture loss must reflect actually dropped bytes: bytes=%d truncated=%v system=%d result=%d", diagnostics.Bytes, diagnostics.Truncated, diagnostics.Events["system"], diagnostics.Events["result"])
			}
		})
	}
}

func TestProviderDiagnosticsRetainParserTruncation(t *testing.T) {
	for name, output := range map[string]string{
		"long_line":    strings.Repeat("x", 64*1024+1),
		"many_records": strings.Repeat("{}\n", 4097),
	} {
		t.Run(name, func(t *testing.T) {
			diagnostics := providerDiagnostics(output)
			if !diagnostics.Truncated || diagnostics.Bytes != len(output) {
				t.Fatal("parser truncation lost without capture overflow")
			}
		})
	}
}
