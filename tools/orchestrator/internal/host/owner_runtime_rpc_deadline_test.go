package host

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

func TestOwnerRuntimeControlRPCDeadlineClosesStalledUnixPeer(t *testing.T) {
	for _, kind := range []string{"state", "attach"} {
		t.Run(kind, func(t *testing.T) {
			e, b, _ := ownerEnrollmentFixture(t)
			dir, err := os.MkdirTemp("", "owner-rpc-deadline-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			socket := filepath.Join(dir, "o.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			accepted := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				if _, err = ipc.Read(conn); err != nil {
					done <- err
					return
				}
				close(accepted)
				var one [1]byte
				_, err = conn.Read(one[:])
				done <- err
			}()
			e.client.SocketPath = socket
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			started := time.Now()
			_, err = callOwnerRuntimeOperation(ctx, e.client.Call, b.Permit.ControlRevision, kind, "stalled-"+kind, struct{}{})
			elapsed := time.Since(started)
			select {
			case <-accepted:
			default:
				t.Fatal("fixture never reached actual Unix peer")
			}
			if err == nil || elapsed > 3*time.Second || elapsed < 1500*time.Millisecond || ctx.Err() != nil {
				t.Fatal("RPC did not honor separate two-second operation deadline", elapsed, err, ctx.Err())
			}
			select {
			case err = <-done:
				if err == nil {
					t.Fatal("peer read completed unexpectedly")
				}
			case <-time.After(time.Second):
				t.Fatal("deadline did not close actual client socket")
			}
		})
	}
}

func TestOwnerRuntimeControlRPCPreservesRequestAndCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	wantDeadline, _ := ctx.Deadline()
	body := contract.OwnerAttachRequest{ExpectedControlRevision: 1}
	reply := contract.OwnerRPCReply{Version: 1, RequestID: "exact-pending"}
	failure := errors.New("signed semantic refusal")
	got, err := callOwnerRuntimeOperation(ctx, func(op context.Context, revision uint64, kind, id string, payload any) (contract.OwnerRPCReply, error) {
		d, _ := op.Deadline()
		if d != wantDeadline || revision != 1 || kind != "attach" || id != "exact-pending" || payload != body {
			t.Fatal("operation changed immutable pending request")
		}
		return reply, failure
	}, 1, "attach", "exact-pending", body)
	if got.RequestID != reply.RequestID || err != failure {
		t.Fatal("operation swallowed reply/refusal")
	}
}
