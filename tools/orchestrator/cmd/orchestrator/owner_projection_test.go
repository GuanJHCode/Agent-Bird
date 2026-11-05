package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
)

func TestOwnerBindLegacyCoordinatorDoesNotReceiveProjectionExtension(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "legacy-owner-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(root, "coordinator.sock"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				done <- nil
				return
			}
			request, err := ipc.Read(conn)
			if err != nil {
				_ = conn.Close()
				done <- err
				return
			}
			var body json.RawMessage
			switch request.Kind {
			case ipc.KindReady:
				body = json.RawMessage(`{"status":"ready","epoch":1,"capabilities":[]}`)
			case ipc.KindOwnerBind:
				var old struct {
					ControllerThread string `json:"controller_thread"`
					OriginPID        int    `json:"origin_pid"`
					OriginBirth      string `json:"origin_birth"`
				}
				if err := decode(request.Payload, &old); err != nil {
					_ = conn.Close()
					done <- err
					return
				}
				body = json.RawMessage(`{"version":1,"owner_mode":"local","owner_capability":"/private/legacy-owner","controller_thread":"legacy","origin_pid":2,"origin_birth":"birth","origin_context_id":"origin","host_generation":"generation"}`)
			default:
				_ = conn.Close()
				done <- codeError("unexpected_kind")
				return
			}
			err = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindResponse, RequestID: request.RequestID, Epoch: 1, Payload: body})
			_ = conn.Close()
			if err != nil {
				done <- err
				return
			}
		}
	}()
	defer func() {
		_ = listener.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	request := filepath.Join(root, "request.json")
	if err = writeExclusiveJSON(request, coordinator.OwnerBindRequest{ControllerThread: "legacy", OriginPID: os.Getpid(), OriginBirth: birth}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = ownerBind(context.Background(), []string{"--state-dir", root, "--request", request}, &out); err != nil {
		t.Fatal(err)
	}
	var shared coordinator.OwnerBindResponse
	if err = decode(out.Bytes(), &shared); err != nil {
		t.Fatal(err)
	}
	if shared.OwnerScope != "" || shared.OwnerCapabilities != nil {
		t.Fatal("legacy coordinator was given fabricated capability projection")
	}
}
