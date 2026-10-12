package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestPendingOwnerRPCPreservesAuthorityAndSharedBudget(t *testing.T) {
	for _, kind := range []string{"lost_reply", "signed_refusal", "authentication_failure", "exhausted", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			recovery, err := openOwnerRecovery(privateRecoveryTestDirectory(t), strings.Repeat("a", 64))
			if err != nil {
				t.Fatal(err)
			}
			defer recovery.close()
			old := contract.OriginalOwnerTransport{PID: 10, Birth: "old"}
			child := contract.OriginalOwnerTransport{PID: 20, Birth: "child"}
			if _, err = recovery.reserveTransport(time.Unix(1, 0), "original-request", 1, old); err != nil {
				t.Fatal(err)
			}
			if err = recovery.beginTransportStart(); err != nil {
				t.Fatal(err)
			}
			if err = recovery.recordTransportStarted(child); err != nil {
				t.Fatal(err)
			}
			pending, err := recovery.ticketPending()
			if err != nil {
				t.Fatal(err)
			}
			original, _ := json.Marshal(recovery.record.OwnerPending)
			if kind == "exhausted" {
				for i := 0; i < 4; i++ {
					if _, err = recovery.reserveCoordinator(time.Unix(int64(100*(i+1)), 0)); err != nil {
						t.Fatal(err)
					}
					if err = recovery.beginCoordinatorEnsure(); err != nil {
						t.Fatal(err)
					}
					if err = recovery.finishCoordinatorEnsure(); err != nil {
						t.Fatal(err)
					}
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls, ensures := 0, 0
			cfg := OriginalOwnerConfig{EnsureCoordinator: func(context.Context) error { ensures++; return nil }}
			call := func(operation context.Context, revision uint64, rpc, id string, body any) (contract.OwnerRPCReply, error) {
				calls++
				deadline, ok := operation.Deadline()
				got, bodyOK := body.(contract.OwnerTransportPending)
				if !ok || time.Until(deadline) > 2*time.Second || revision != 1 || rpc != "ticket" || id != "original-request" || !bodyOK || got != pending {
					t.Fatal("retry changed request authority or lost operation bound")
				}
				if calls == 2 {
					return contract.OwnerRPCReply{Version: 1}, nil
				}
				switch kind {
				case "signed_refusal":
					return contract.OwnerRPCReply{Version: 1, Error: "EOF"}, io.EOF
				case "authentication_failure":
					return contract.OwnerRPCReply{}, errors.New("original_owner_reply_rejected")
				case "canceled":
					cancel()
					return contract.OwnerRPCReply{}, io.EOF
				default:
					return contract.OwnerRPCReply{}, io.EOF
				}
			}
			_, err = callPendingOwnerRPC(ctx, cfg, recovery, call, 1, "ticket", pending.RequestID, pending)
			wantAttempts := 1
			if kind == "lost_reply" {
				wantAttempts = 2
				if err != nil || calls != 2 || ensures != 1 {
					t.Fatal("lost reply did not replay same request", calls, ensures, err)
				}
			} else {
				if err == nil || calls != 1 || ensures != 0 {
					t.Fatal("unsafe or exhausted retry admitted", calls, ensures, err)
				}
				if kind == "exhausted" {
					wantAttempts = 5
					if !recovery.record.Unavailable {
						t.Fatal("sixth reserve not durably sealed")
					}
				}
			}
			after, _ := json.Marshal(recovery.record.OwnerPending)
			if string(after) != string(original) || recovery.record.Attempts != wantAttempts {
				t.Fatal("reply repair replaced child/start slot or refunded budget")
			}
		})
	}
}
