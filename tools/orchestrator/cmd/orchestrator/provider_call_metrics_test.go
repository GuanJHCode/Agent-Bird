package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProviderCallMetricsRemainOutOfModelView(t *testing.T) {
	result, err := executeProviderCall(context.Background(), providerCallRequest{Version: 1, Provider: "invalid", Task: "private task", Directory: "/private/project"}, "")
	if err == nil || result.ProcessExited {
		t.Fatal("invalid call became executable")
	}
	raw, _ := json.Marshal(result)
	var cli map[string]any
	if json.Unmarshal(raw, &cli) != nil {
		t.Fatal("invalid CLI result")
	}
	timings, ok := cli["timings_ms"].(map[string]any)
	if !ok || timings["total"] == nil {
		t.Fatalf("CLI lacks timings: %s", raw)
	}
	raw, _ = json.Marshal(compactProviderResult(result))
	var model map[string]any
	if json.Unmarshal(raw, &model) != nil {
		t.Fatal("invalid model result")
	}
	if _, ok := model["timings_ms"]; ok {
		t.Fatal("diagnostic overhead leaked into model context")
	}
}

func TestProviderCallMetricsArePrivateAndNeverOverwrite(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	result := providerCallResult{CallID: "call-test", Provider: "grok", Handle: filepath.Join(root, "handle.json"), Status: "result_ready", ProcessExited: true}
	metrics := newProviderCallMetrics()
	metrics.mark("prepare")
	metrics.finish(&result, nil)
	path := filepath.Join(root, "call-metrics.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private metrics missing", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if json.Unmarshal(before, &record) != nil || record["call_id"] != "call-test" || record["process_exited"] != true || record["timings_ms"] == nil {
		t.Fatal(string(before))
	}
	result.Status = "failed"
	metrics.finish(&result, codeError("later"))
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("metrics overwritten", err)
	}
}
