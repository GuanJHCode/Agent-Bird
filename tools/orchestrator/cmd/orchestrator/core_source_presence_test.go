package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestCoreEnsureSourcePresenceNeverSpawnsAliveOrUnprovedHost(t *testing.T) {
	for _, name := range []string{"alive_reconciles", "unknown", "inspection_error", "canceled"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "canceled" {
				cancel()
			}
			starts, polls := 0, 0
			err := ensureSourceHostPresence(ctx, func(context.Context) (contract.HostPresenceResponse, error) {
				polls++
				if name == "inspection_error" {
					return contract.HostPresenceResponse{}, errors.New("probe failed")
				}
				if name == "unknown" {
					return contract.HostPresenceResponse{Status: "offline", Presence: "unknown"}, nil
				}
				status := "reconciling"
				if polls == 2 {
					status = "ready"
				}
				return contract.HostPresenceResponse{Status: status, Presence: "same_host_alive"}, nil
			}, func(context.Context) error { starts++; return nil })
			if starts != 0 {
				t.Fatalf("unproved/alive Host spawned %d times", starts)
			}
			if (err == nil) != (name == "alive_reconciles") {
				t.Fatalf("unexpected err=%v", err)
			}
			if name == "alive_reconciles" && polls != 2 {
				t.Fatal("did not observe real ready transition")
			}
		})
	}
}

func TestCoreEnsureSourcePresenceColdStartIsOnceWithinOriginalWindow(t *testing.T) {
	for _, name := range []string{"unregistered", "old_host_exited"} {
		t.Run(name, func(t *testing.T) {
			starts, polls := 0, 0
			var original time.Time
			err := ensureSourceHostPresence(context.Background(), func(ctx context.Context) (contract.HostPresenceResponse, error) {
				deadline, _ := ctx.Deadline()
				if original.IsZero() {
					original = deadline
				} else if !original.Equal(deadline) {
					t.Fatal("poll renewed original deadline")
				}
				polls++
				if polls < 3 {
					return contract.HostPresenceResponse{Status: "offline", Presence: name}, nil
				}
				return contract.HostPresenceResponse{Status: "ready", Presence: "same_host_alive"}, nil
			}, func(ctx context.Context) error {
				deadline, _ := ctx.Deadline()
				if !deadline.Equal(original) {
					t.Fatal("startup renewed original deadline")
				}
				starts++
				return nil
			})
			if err != nil || starts != 1 || polls != 3 {
				t.Fatalf("cold start err=%v starts=%d polls=%d", err, starts, polls)
			}
		})
	}
}

func TestCoreSourceStartupRejectsCanceledOriginalWindowBeforeLaunch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Invalid bootstrap deliberately follows the context check. No process,
	// pipe, log or state directory may be opened by a canceled startup.
	if err := startSourceHostContext(ctx, "/invalid/owned-test", t.TempDir(), hostBootstrap{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("startup lost original cancellation: %v", err)
	}
}
