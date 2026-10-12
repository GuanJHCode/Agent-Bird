package process

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
)

func TestCodexRPCWithDuplexRealOSStreams(t *testing.T) {
	t.Run("roundtrip", func(t *testing.T) {
		h, stream, ctx := ownDuplex(t, duplexCommand(t, "rpc"))
		client, err := codexrpc.New(ctx, stream, codexrpc.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		params := json.RawMessage(`{"integer":9007199254740993,"text":"owned fixture"}`)
		result, err := client.Call(ctx, "fixture/echo", params)
		if err != nil || !bytes.Equal(result, params) {
			t.Fatalf("OS pipe RPC changed exact reply: bytes=%d err=%v", len(result), err)
		}
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		if err := h.Wait(ctx); err != nil {
			t.Fatal(err)
		}
		if h.Output() != "" || h.ConfirmTreeExited() != nil {
			t.Fatal("RPC consumed raw stdout twice or lost original exit proof")
		}
	})
	t.Run("cancel_dispatched_call", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "dispatched")
		spec := duplexCommand(t, "rpc")
		spec.Env = append(spec.Env, "C06_DUPLEX_DISPATCHED="+marker)
		h, stream, ctx := ownDuplex(t, spec)
		client, err := codexrpc.New(ctx, stream, codexrpc.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		callCtx, cancelCall := context.WithCancel(ctx)
		defer cancelCall()
		called := make(chan error, 1)
		go func() { _, err := client.Call(callCtx, "fixture/block", json.RawMessage(`{}`)); called <- err }()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			if content, err := os.ReadFile(marker); err == nil && string(content) == "received" {
				break
			}
			select {
			case err := <-called:
				t.Fatalf("call completed before real child dispatch barrier: %v", err)
			case <-ctx.Done():
				t.Fatal("real OS child never received RPC")
			case <-ticker.C:
			}
		}
		cancelCall()
		select {
		case err := <-called:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("dispatched cancellation lost context: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancellation did not close real OS streams")
		}
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		if h.ConfirmTreeExited() == nil {
			t.Fatal("RPC cancellation became a process exit receipt")
		}
		stopCtx, cancelStop := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelStop()
		if err := h.Stop(stopCtx); err != nil {
			t.Fatalf("original Stop after transport cancellation: %v", err)
		}
		if err := h.Wait(stopCtx); err != nil || h.ConfirmTreeExited() != nil {
			t.Fatalf("original Wait/group proof: %v", err)
		}
	})
}
