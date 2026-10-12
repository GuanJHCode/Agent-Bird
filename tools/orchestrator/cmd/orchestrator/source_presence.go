package main

import (
	"context"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func ensureSourceHostPresence(parent context.Context, inspect func(context.Context) (contract.HostPresenceResponse, error), start func(context.Context) error) error {
	// One original CLI window spans observation, possible cold startup and
	// reconciliation. Polling never spawns again or resets the Host budget.
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	started := false
	for {
		if ctx.Err() != nil {
			if parent.Err() != nil {
				return parent.Err()
			}
			return codeError("source_host_start_timeout")
		}
		presence, err := inspect(ctx)
		if err != nil {
			return err
		}
		switch presence.Presence {
		case "same_host_alive":
			if presence.Status == "ready" {
				return nil
			}
		case "unregistered", "old_host_exited":
			if !started {
				if err := start(ctx); err != nil {
					return err
				}
				started = true
			}
		default:
			return codeError("host_recovery_identity_unknown")
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}
