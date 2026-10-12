package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

// Owned by one serial call. Only authenticated read operations reuse Ready;
// mutations retain the ordinary path. Never replay an operation or silently
// adopt a replacement coordinator after the original epoch has been bound.
func newBoundProviderCallReader(state string) providerCallReader {
	var epoch uint64
	return func(ctx context.Context, kind ipc.Kind, payload any) (json.RawMessage, error) {
		if kind != ipc.KindStatus && kind != ipc.KindCallResult && kind != ipc.KindWaitCallTerminal {
			response, err := call(ctx, state, kind, payload)
			return response.Payload, err
		}
		if epoch == 0 {
			ready, err := ensureServer(ctx, state)
			if err != nil {
				return nil, err
			}
			var ok bool
			epoch, ok = ready["epoch"].(uint64)
			if !ok || epoch == 0 {
				return nil, ipc.ErrInvalidMessage
			}
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		id := fmt.Sprintf("call-read-%d-%d", os.Getpid(), time.Now().UnixNano())
		response, err := ipc.Call(ctx, filepath.Join(state, "coordinator.sock"), ipc.Envelope{Version: ipc.Version, Kind: kind, RequestID: id, Epoch: epoch, Payload: body})
		if err != nil {
			return nil, err
		}
		if response.Epoch != epoch {
			return nil, codeError("call_coordinator_changed")
		}
		if response.RequestID != id {
			return nil, ipc.ErrInvalidMessage
		}
		if response.Kind == ipc.KindError {
			var failure coordinator.ErrorResponse
			if decode(response.Payload, &failure) != nil || failure.Error == "" {
				return nil, codeError("remote_error")
			}
			return nil, remoteError(failure.Error)
		}
		if response.Kind != ipc.KindResponse {
			return nil, ipc.ErrInvalidMessage
		}
		return response.Payload, nil
	}
}
