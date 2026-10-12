package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProviderCallCLICompactKeepsFailureFacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"provider":"invalid","task":"check","directory":"/private/project"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, compact := range []bool{false, true} {
		args := []string{"--request", path}
		if compact {
			args = append(args, "--compact")
		}
		var out bytes.Buffer
		if err := providerCallEntry(context.Background(), args, &out); err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result["status"] != "failed" || result["failure_reason"] != "call_provider_unsupported" || result["process_exited"] != false {
			t.Fatal(result)
		}
		_, hasView := result["result_truncated"]
		_, hasTimings := result["timings_ms"]
		if hasView != compact || hasTimings == compact {
			t.Fatal("wrong result view", result)
		}
	}
}
