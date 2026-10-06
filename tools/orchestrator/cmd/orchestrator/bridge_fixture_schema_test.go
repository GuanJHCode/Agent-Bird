package main

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
)

// Only the just-built offline worker branch runs, with no binding/hook env.
// This proves fixture protocol behavior, not provider/model or group adoption.
func TestBridgeWorkerFixturesHonorFixedManagedSchema(t *testing.T) {
	for _, provider := range []adapter.Provider{adapter.ProviderAGY, adapter.ProviderClaude} {
		t.Run(string(provider), func(t *testing.T) {
			root := privateTaskTemp(t)
			source := "testdata/agy/agy_fixture.go"
			workerArgs := []string{"--output-format", "stream-json"}
			if provider == adapter.ProviderClaude {
				source = "testdata/b05/claude_fixture.go"
				workerArgs = append([]string{"--print"}, workerArgs...)
			}
			bin := filepath.Join(root, "worker")
			buildCtx, cancelBuild := context.WithTimeout(context.Background(), 30*time.Second)
			body, err := exec.CommandContext(buildCtx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-p", "1", "-o", bin, source).CombinedOutput()
			cancelBuild()
			if err != nil {
				t.Fatalf("owned fixture build: %v %s", err, body)
			}
			for _, name := range []string{"valid", "missing", "malformed", "different_shape", "duplicate_option", "oversized"} {
				t.Run(name, func(t *testing.T) {
					args := append([]string(nil), workerArgs...)
					schema := adapter.ManagedTaskSchema()
					switch name {
					case "malformed":
						schema = "{"
					case "different_shape":
						schema = `{"type":"object"}`
					case "oversized":
						schema = strings.Repeat("x", 4097)
					}
					if name != "missing" {
						args = append(args, "--json-schema", schema)
					}
					if name == "duplicate_option" {
						args = append(args, "--json-schema", schema)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					cmd := exec.CommandContext(ctx, bin, args...)
					cmd.Dir = root
					cmd.Env = []string{"HOME=" + root, "TMPDIR=" + root, "PATH=/usr/bin:/bin"}
					var output, diagnostics bytes.Buffer
					cmd.Stdout, cmd.Stderr = &output, &diagnostics
					err := cmd.Run() // owns the original process and Wait
					if name != "valid" {
						if err == nil || output.Len() != 0 || strings.TrimSpace(diagnostics.String()) != "fixture_managed_schema_invalid" {
							t.Fatalf("invalid schema reached terminal: err=%v output=%q diagnostics=%q", err, output.String(), diagnostics.String())
						}
						return
					}
					if err != nil || diagnostics.Len() != 0 || output.Len() > 4096 {
						t.Fatalf("valid schema rejected: err=%v diagnostics=%q", err, diagnostics.String())
					}
					lines := strings.Split(strings.TrimSpace(output.String()), "\n")
					if len(lines) != 2 {
						t.Fatal("fixture did not return exact init/terminal")
					}
					init, err := adapter.ParseEvent(provider, lines[0], 4096)
					if err != nil || init.Kind != adapter.EventInit || init.SessionID != "fixture-worker" {
						t.Fatal("invalid native init", init, err)
					}
					terminal, err := adapter.ParseEvent(provider, lines[1], 4096)
					if err != nil || terminal.Kind != adapter.EventResult || terminal.SessionID != init.SessionID || terminal.NativeFailure || terminal.NativePermissionDenied || terminal.NativeMetadataInvalid {
						t.Fatal("invalid native terminal", terminal, err)
					}
					completion, err := adapter.DecodeTaskCompletion(terminal.StructuredText, 4096)
					original := `中文结果：\"引号\"与反斜线\\`
					if err != nil || completion.Kind != "result" || completion.Text != original || terminal.Text != original {
						t.Fatal("original logical text or structured completion lost", completion, terminal, err)
					}
				})
			}
		})
	}
}
