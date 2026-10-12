package main

import (
	"context"
	"encoding/json"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"strings"
	"testing"
)

func TestProviderCallRequestUsesOneRoutingContract(t *testing.T) {
	for _, provider := range []string{"codex", "claude", "agy", "grok"} {
		req := providerCallRequest{Version: 1, Provider: provider, Directory: "/private/project", Task: "Read README and explain the entry point."}
		if err := normalizeProviderCall(&req); err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		if req.Mode != "read-only" || req.TimeoutMS != 120000 {
			t.Fatal("unsafe or missing defaults")
		}
	}
}

func TestProviderCallRejectsUnscopedWriteAndRetryExpansion(t *testing.T) {
	for _, kind := range []string{"unknown_provider", "unscoped_write", "readonly_paths", "escape", "metadata", "timeout", "empty_task"} {
		t.Run(kind, func(t *testing.T) {
			r := providerCallRequest{Version: 1, Provider: "claude", Directory: "/private/project", Task: "Fix the function", Mode: "read-only", TimeoutMS: 120000}
			switch kind {
			case "unknown_provider":
				r.Provider = "auto"
			case "unscoped_write":
				r.Mode = "workspace-write"
			case "readonly_paths":
				r.Paths = []string{"file.go"}
			case "escape":
				r.Mode = "workspace-write"
				r.Paths = []string{"../file.go"}
			case "metadata":
				r.Mode = "workspace-write"
				r.Paths = []string{".GiT/config"}
			case "timeout":
				r.TimeoutMS = 3600001
			case "empty_task":
				r.Task = " "
			}
			if normalizeProviderCall(&r) == nil {
				t.Fatal("invalid call admitted")
			}
		})
	}
}

func TestProviderCallReassemblesBoundResultPages(t *testing.T) {
	for _, drift := range []bool{false, true} {
		text := strings.Repeat("多", 4000)
		read := func(ctx context.Context, kind ipc.Kind, request any) (json.RawMessage, error) {
			if kind == ipc.KindStatus {
				return json.Marshal(store.TaskSnapshot{SegmentStatus: "exited"})
			}
			offset := request.(coordinator.CallResultRequest).Offset
			end := offset + 6000
			if end > len(text) {
				end = len(text)
			}
			hash := strings.Repeat("a", 64)
			if drift && offset > 0 {
				hash = strings.Repeat("b", 64)
			}
			return json.Marshal(store.CallResult{Version: 1, Status: "result_ready", ProcessExited: true, EventKind: "result", PayloadSHA256: hash, Content: text[offset:end], NextOffset: end, TotalBytes: len(text)})
		}
		got, err := awaitProviderCall(context.Background(), coordinator.TaskControlRequest{TaskID: "one"}, read)
		if drift {
			if err == nil {
				t.Fatal("accepted changed receipt between pages")
			}
		} else if err != nil || got.Content != text {
			t.Fatalf("reassembly: %v", err)
		}
	}
}

func TestProviderCallUnknownAndCancellationDoNotClaimExit(t *testing.T) {
	for _, status := range []string{"unknown", "running"} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		read := func(context.Context, ipc.Kind, any) (json.RawMessage, error) {
			return json.Marshal(store.TaskSnapshot{SegmentStatus: status})
		}
		got, err := awaitProviderCall(ctx, coordinator.TaskControlRequest{}, read)
		if err == nil || got.ProcessExited {
			t.Fatalf("%s: %+v %v", status, got, err)
		}
	}
}

func TestCodexInitiatorDoesNotOpenCodexWorker(t *testing.T) {
	if _, name, err := controllerProvider("codex"); err != nil || name != "codex" {
		t.Fatal(name, err)
	}
	if _, _, err := taskProvider("codex"); err == nil {
		t.Fatal("initiator admission opened Worker")
	}
}

func TestProviderCallAndMCPArePluginActions(t *testing.T) {
	if !pluginActionAllowed("call") || !pluginActionAllowed("mcp") {
		t.Fatal("unified entry missing from plugin")
	}
}
