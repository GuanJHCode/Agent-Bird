package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestOwnerCommandDispositionSeparatesStaleRecoveryAndLocalFailure(t *testing.T) {
	for _, kind := range []string{"stale", "new_epoch", "tampered_new_epoch", "local_failure"} {
		t.Run(kind, func(t *testing.T) {
			cfg, first, key := ownerExecutionFixture(t)
			cfg.rpc = func(_ context.Context, _ uint64, kind, id string, body any) (contract.OwnerRPCReply, error) {
				applied := body.(contract.OwnerPermitApplied)
				status := "applied"
				if kind == "activate" {
					status = "active"
				}
				raw, _ := json.Marshal(map[string]string{"status": status})
				return contract.OwnerRPCReply{Version: 1, OwnerID: first.OwnerID, CoordinatorEpoch: applied.Permit.CoordinatorEpoch, RequestID: id, Body: raw}, nil
			}
			e, err := newOwnerExecution(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer e.run.cancel(context.Canceled)
			if err = e.reconcile(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			next := first
			next.ControlRevision++
			next.ExpectedControlRevision++
			next.OldHost = &first.NewHost
			next.CoordinatorEpoch++
			next.RequestID = "renewal"
			next.PermitID = "permit-2"
			next, err = contract.SignOwnerControlPermit(next, key)
			if err != nil {
				t.Fatal(err)
			}
			var raw []byte
			expected := ownerCommandInvalid
			diskFailure := errors.New("injected disk failure")
			switch kind {
			case "stale":
				if err = e.reconcile(context.Background(), next); err != nil {
					t.Fatal(err)
				}
				raw = ownerExecutionSignedLaunch(t, first, key)
				expected = ownerCommandStale
			case "new_epoch":
				raw = ownerExecutionSignedLaunch(t, next, key)
				expected = ownerCommandRecover
			case "tampered_new_epoch":
				raw = bytes.Replace(ownerExecutionSignedLaunch(t, next, key), []byte(`"task_id":"task"`), []byte(`"task_id":"altered"`), 1)
			case "local_failure":
				raw = ownerExecutionSignedLaunch(t, first, key)
				e.acceptGrant = func(contract.LaunchCommand) (bool, error) { return false, diskFailure }
				expected = ownerCommandLocalFailure
			}
			err = e.command(raw)
			if ownerCommandDisposition(err) != expected {
				t.Fatalf("%s action=%v err=%v", kind, ownerCommandDisposition(err), err)
			}
			if kind == "local_failure" && !errors.Is(err, diskFailure) {
				t.Fatal("local durable failure cause lost")
			}
			if kind == "new_epoch" {
				if e.observedEpoch != next.CoordinatorEpoch || e.admitSignedLaunch || e.active {
					t.Fatal("new signed epoch did not revoke old admission")
				}
				select {
				case recovery := <-e.recovery:
					if !ownerExecutionNeedsRecovery(recovery) {
						t.Fatal("epoch proof lost typed recovery")
					}
				default:
					t.Fatal("epoch proof did not notify owner")
				}
				if ownerCommandDisposition(e.command(ownerExecutionSignedLaunch(t, first, key))) != ownerCommandStale {
					t.Fatal("revoked old epoch reopened admission")
				}
				terminal, signErr := contract.SignOwnerCommand(contract.OwnerCommand{Version: 1, Permit: first, Kind: "terminal", RequestID: "old-terminal", Body: json.RawMessage(`{"reason":"host_retired"}`)}, key)
				if signErr != nil {
					t.Fatal(signErr)
				}
				terminalRaw, _ := json.Marshal(terminal)
				if ownerCommandDisposition(e.command(terminalRaw)) != ownerCommandStale || e.stopping {
					t.Fatal("old terminal terminated recovering owner")
				}

			} else {
				if kind == "local_failure" {
					if !e.stopping || e.active || e.admitSignedLaunch {
						t.Fatal("local durable failure did not seal admission")
					}
					e.beginStop(context.Background(), diskFailure)
					if !errors.Is(e.join(context.Background()), diskFailure) {
						t.Fatal("cleanup hid durable admission failure")
					}
				} else if !e.active || !e.admitSignedLaunch {
					t.Fatal("refused frame revoked healthy authority")
				}
				select {
				case <-e.recovery:
					t.Fatal("refused frame charged recovery")
				default:
				}
			}
			if _, err := os.Stat(filepath.Join(cfg.host.spoolRoot, ".grants")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("rejected command wrote grant authority", err)
			}
		})
	}
}
