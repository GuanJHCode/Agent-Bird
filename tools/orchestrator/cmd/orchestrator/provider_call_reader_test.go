package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

func TestBoundProviderReaderChecksReadyOnceAndRejectsChangedEpoch(t *testing.T) {
	for _, mode := range []string{"same", "changed", "wrong_id", "cancel", "stop"} {
		t.Run(mode, func(t *testing.T) {
			root, err := os.MkdirTemp("", "bird-rpc-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(root)
			root, err = filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("unix", filepath.Join(root, "coordinator.sock"))
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			ready, requests := 0, 0
			done := make(chan struct{})
			second := make(chan struct{})
			go func() {
				defer close(done)
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					message, err := ipc.Read(conn)
					if err != nil {
						conn.Close()
						return
					}
					mu.Lock()
					epoch := uint64(7)
					body := json.RawMessage(`{"status":"ok"}`)
					if message.Kind == ipc.KindReady {
						ready++
						body = json.RawMessage(`{"status":"ready","epoch":7}`)
					} else {
						requests++
						if mode == "changed" && requests == 2 {
							epoch = 8
						}
					}
					isSecond := message.Kind != ipc.KindReady && requests == 2
					mu.Unlock()
					if isSecond && mode == "cancel" {
						close(second)
						_, _ = io.Copy(io.Discard, conn)
						conn.Close()
						continue
					}
					id := message.RequestID
					if isSecond && mode == "wrong_id" {
						id = "foreign-response"
					}
					_ = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindResponse, RequestID: id, Epoch: epoch, Payload: body})
					conn.Close()
				}
			}()
			defer func() { listener.Close(); <-done }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			reader := newBoundProviderCallReader(root)
			if _, err := reader(ctx, ipc.KindStatus, map[string]string{"task_id": "one"}); err != nil {
				t.Fatal(err)
			}
			kind := ipc.KindCallResult
			if mode == "stop" {
				kind = ipc.KindStopTask
			}
			if mode == "cancel" {
				kind = ipc.KindWaitCallTerminal
				go func() {
					select {
					case <-second:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			raw, err := reader(ctx, kind, map[string]string{"task_id": "one"})
			if mode == "changed" {
				if errorCode(err) != "call_coordinator_changed" || raw != nil {
					t.Fatal("accepted restarted coordinator", string(raw), err)
				}
			} else if mode == "wrong_id" {
				if !errors.Is(err, ipc.ErrInvalidMessage) || raw != nil {
					t.Fatal("foreign response accepted", err)
				}
			} else if mode == "cancel" {
				if !errors.Is(err, context.Canceled) || raw != nil {
					t.Fatal("cancelled read returned a payload", err)
				}
			} else if err != nil || string(raw) != `{"status":"ok"}` {
				t.Fatal(string(raw), err)
			}
			mu.Lock()
			gotReady, gotRequests := ready, requests
			mu.Unlock()
			wantReady := 1
			if mode == "stop" {
				wantReady = 2
			}
			if gotReady != wantReady || gotRequests != 2 {
				t.Fatalf("Ready=%d, requests=%d", gotReady, gotRequests)
			}
		})
	}
}
