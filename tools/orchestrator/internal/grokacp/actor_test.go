package grokacp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// TestGrokACPActor is an owned, no-account ACP v1 fixture. It only executes
// when the parent native test starts this same test binary with the marker.
func TestGrokACPActor(t *testing.T) {
	if os.Getenv("GROKACP_NATIVE_ACTOR") != "1" {
		return
	}
	reader := bufio.NewScanner(os.Stdin)
	reader.Buffer(make([]byte, 4096), MaxFrameBytes)
	for reader.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(reader.Bytes(), &request) != nil {
			os.Exit(21)
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}
		case "session/new":
			result = map[string]any{"sessionId": "fixture-session"}
		case "session/prompt":
			result = map[string]any{"stopReason": "end_turn"}
		default:
			os.Exit(22)
		}
		body, _ := json.Marshal(struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  any             `json:"result"`
		}{"2.0", request.ID, result})
		if _, err := os.Stdout.Write(append(body, '\n')); err != nil {
			os.Exit(23)
		}
	}
	if reader.Err() != nil {
		os.Exit(24)
	}
}

// This is intentionally opt-in: it proves only the existing StartDuplex
// hookup retains its original Handle across two ACP turns and Close/Wait. It
// never starts Grok and is not included in the pure transport acceptance.
func TestGrokACPStartDuplexOwnedActor(t *testing.T) {
	if os.Getenv("GROKACP_NATIVE") != "1" {
		t.Skip("native process token required")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("StartDuplex unsupported")
	}
	binary, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	handle, stream, err := process.StartDuplex(ctx, process.Command{Path: binary, Args: []string{"-test.run=^TestGrokACPActor$"}, ExactEnv: true, Env: []string{"GROKACP_NATIVE_ACTOR=1"}})
	if err != nil || handle == nil || stream == nil {
		t.Fatalf("StartDuplex handle=%v stream=%v err=%v", handle, stream, err)
	}
	client, err := New(ctx, stream, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	sessionID, err := client.NewSession(ctx, "/private/work")
	if err != nil || sessionID != "fixture-session" {
		t.Fatalf("NewSession id=%q err=%v", sessionID, err)
	}
	for _, text := range []string{"one", "two"} {
		turn, err := client.Prompt(ctx, sessionID, text)
		if err != nil || turn.StopReason != "end_turn" {
			t.Fatalf("Prompt(%q) turn=%#v err=%v", text, turn, err)
		}
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := handle.Wait(ctx); err != nil || handle.ExitCode() != 0 {
		t.Fatalf("original Wait error=%v exit=%d", err, handle.ExitCode())
	}
	if err := handle.ConfirmTreeExited(); err != nil {
		t.Fatalf("original process tree = %v", err)
	}
}
