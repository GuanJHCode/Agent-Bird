package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
)

// Synthetic stderr fixtures exercise the diagnostic projection below native
// admission; they do not launch Codex or access real authentication state.
func TestCodexGuardFailureArtifact(t *testing.T) {
	for _, tc := range []struct{ name, guard, code string }{
		{"known", `{"type":"codex_auth_guard","code":"native_workspace_write_unverified"}`, "native_workspace_write_unverified"},
		{"extra private fields", `{"type":"codex_auth_guard","code":"codex_policy_untrusted","message":"PRIVATE_SENTINEL","token":"PRIVATE_SENTINEL"}`, "codex_policy_untrusted"},
		{"unknown", `{"type":"codex_auth_guard","code":"PRIVATE_SENTINEL","message":"PRIVATE_SENTINEL"}`, ""},
		{"truncated", `{"type":"codex_auth_guard","code":"codex_policy_untrusted"`, ""},
		{"ready only", `{"type":"codex_auth_guard","code":"ready"}`, ""},
		{"native error", `{"type":"error","message":"PRIVATE_SENTINEL"}`, ""},
		{"raw stderr", `PRIVATE_SENTINEL`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "spool")
			h, err := NewIPC(root, "producer")
			if err != nil {
				t.Fatal(err)
			}
			// No newline intentionally covers the stderr EOF record.
			result, err := h.execute(context.Background(), store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}, "run", "task", process.Command{Path: "/bin/sh", Args: []string{"-c", `printf '%s' "$1" >&2; exit 1`, "fixture", tc.guard}}, false, launchMetadata{outputProvider: "codex-cli"})
			if err != nil || result.Status != "failed" || result.ExitCode != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			sink := &durableSink{}
			if err := h.PublishAll(context.Background(), sink, 1); err != nil {
				t.Fatal(err)
			}
			var failed, exited *contract.Event
			for i := range sink.events {
				e := &sink.events[i]
				if e.Kind == contract.EventFailed {
					failed = e
				}
				if e.Kind == contract.EventExited {
					exited = e
				}
			}
			if failed == nil || exited == nil || failed.TaskID != "task" || failed.SegmentID != "segment" {
				t.Fatalf("missing bound failure/exited events: %+v", sink.events)
			}
			if tc.code == "" {
				if failed.Artifact != nil || result.ArtifactPath != "" {
					t.Fatalf("private/unknown diagnostic became artifact: %+v", failed)
				}
			} else {
				if failed.Artifact == nil || failed.Artifact.Path != result.ArtifactPath {
					t.Fatalf("missing collectible artifact: %+v", failed)
				}
				body, err := os.ReadFile(failed.Artifact.Path)
				want := `{"status":"failed","reason":"codex_auth_guard","code":"` + tc.code + `"}`
				if err != nil || string(body) != want {
					t.Fatalf("artifact=%s err=%v want=%s", body, err, want)
				}
			}
			body, err := os.ReadFile(filepath.Join(root, "attempt", "segment", "provider-diagnostics.json"))
			if err != nil || strings.Contains(string(body), "PRIVATE_SENTINEL") {
				t.Fatalf("unsafe diagnostics=%s err=%v", body, err)
			}
			if tc.code != "" && !strings.Contains(string(body), `"`+tc.code+`":1`) {
				t.Fatalf("code missing from diagnostics: %s", body)
			}
		})
	}
}

func TestCodexGuardDiagnosticsRequireCodexProvider(t *testing.T) {
	input := `{"type":"codex_auth_guard","code":"codex_policy_untrusted","result":{"secret":"PRIVATE_SENTINEL"},"event":["PRIVATE_SENTINEL"],"message":"PRIVATE_SENTINEL"}` + "\n" +
		`{"type":"codex_auth_guard","code":"PRIVATE_SENTINEL"}` + "\n" +
		`{"type":"codex_auth_guard","code":"ready"}`
	for _, provider := range []adapter.Provider{adapter.ProviderCodex, adapter.ProviderClaude, adapter.ProviderGrok, adapter.ProviderAGY} {
		report := providerDiagnosticsFor(provider, input)
		body, _ := json.Marshal(report)
		if strings.Contains(string(body), "PRIVATE_SENTINEL") {
			t.Fatalf("private field escaped: %s", body)
		}
		if provider == adapter.ProviderCodex {
			if report.guardFailureCode != "codex_policy_untrusted" || report.CodexAuthGuard["codex_policy_untrusted"] != 1 || report.CodexAuthGuard["ready"] != 1 || len(report.CodexAuthGuard) != 2 {
				t.Fatalf("unsafe or incomplete codes: %s", body)
			}
		} else if report.guardFailureCode != "" || len(report.CodexAuthGuard) != 0 {
			t.Fatalf("other provider attributed Codex helper: %s", body)
		}
	}
}

func TestCodexGuardReadyDoesNotCreateTerminal(t *testing.T) {
	p, _ := newProtocolCollector("codex-cli")
	_, _ = p.Write([]byte(`{"type":"codex_auth_guard","code":"ready"}` + "\n"))
	if _, _, err := p.Finish(); err == nil || err.Error() != "provider_terminal_missing" {
		t.Fatalf("ready became terminal: %v", err)
	}
	data, err := os.ReadFile("../adapter/testdata/codex-success.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	p, _ = newProtocolCollector("codex-cli")
	_, _ = p.Write([]byte(`{"type":"codex_auth_guard","code":"ready"}` + "\n"))
	_, _ = p.Write(data)
	terminal, _, err := p.Finish()
	if err != nil || terminal.Text != "bounded answer" {
		t.Fatalf("ready disrupted native result: %+v %v", terminal, err)
	}
	encoded, _ := json.Marshal(providerDiagnostics(`{"type":"codex_auth_guard","code":"ready","message":"PRIVATE_SENTINEL"}`))
	if strings.Contains(string(encoded), "PRIVATE_SENTINEL") {
		t.Fatalf("private fields escaped: %s", encoded)
	}
}
