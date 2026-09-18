package coordinator

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProviderControlUsesVerifiedOwnerScope(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "provider-control-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	s, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Serve(ctx)
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	owner := map[string]any{}
	decodePayload(t, callServer(t, s.SocketPath(), ipc.KindOwnerBind, OwnerBindRequest{ControllerThread: "a", OriginPID: os.Getpid(), OriginBirth: birth}), &owner)
	req := map[string]any{"owner_capability": owner["owner_capability"], "provider": "grok-build", "action": "disable"}
	response := callServer(t, s.SocketPath(), ipc.Kind("provider_control"), req)
	var result struct {
		Enabled bool   `json:"enabled"`
		Status  string `json:"status"`
	}
	decodePayload(t, response, &result)
	if result.Enabled || result.Status != "disabled" {
		t.Fatal(result)
	}
	other, err := s.db.ProviderControl(ctx, "b", "grok-build", "status", nil)
	if err != nil || !other.Enabled {
		t.Fatalf("other owner changed: %+v %v", other, err)
	}
	req["controller_thread"] = "b"
	raw, _ := json.Marshal(req)
	denied, err := ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.Kind("provider_control"), RequestID: "spoof", Payload: raw})
	if err != nil || denied.Kind != ipc.KindError {
		t.Fatalf("owner override allowed: %v %v", denied, err)
	}
}
