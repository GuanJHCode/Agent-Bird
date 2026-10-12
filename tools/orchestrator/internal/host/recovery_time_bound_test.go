package host

import (
	"bytes"
	"os"
	"testing"
	"time"
)

func TestRecoveryRejectsFutureTimestampWithoutChangingReservedLedger(t *testing.T) {
	for _, pending := range []bool{true, false} {
		name := "consumed"
		if pending {
			name = "pending"
		}
		t.Run(name, func(t *testing.T) {
			state, err := openHostRecovery(privateRecoveryTestDirectory(t), "scope", 10, "birth")
			if err != nil {
				t.Fatal(err)
			}
			defer state.close()
			state.record.Attempts = 1
			state.record.Pending = pending
			state.record.NextAttemptUnixMS = 100000 + 365*24*60*60*1000
			if err := state.persist(); err != nil {
				t.Fatal(err)
			}
			prior := state.record
			body, err := os.ReadFile(state.path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := state.reserve(time.UnixMilli(100000)); err == nil {
				t.Fatal("year-scale durable next-attempt produced an unbounded wait")
			}
			current, err := os.ReadFile(state.path)
			if err != nil || !bytes.Equal(body, current) || state.record != prior {
				t.Fatal("time validation reset or clamped the original reservation")
			}
		})
	}
}

func TestRecoveryRejectsClockRollbackAfterConsumedAttempt(t *testing.T) {
	state, err := openHostRecovery(privateRecoveryTestDirectory(t), "scope", 10, "birth")
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	if _, err := state.reserve(time.UnixMilli(100000)); err != nil {
		t.Fatal(err)
	}
	if err := state.attemptStarted(); err != nil {
		t.Fatal(err)
	}
	prior := state.record
	if _, err := state.reserve(time.UnixMilli(100500)); err == nil || err.Error() != "host_recovery_clock_regressed" {
		t.Fatalf("rollback extended an already consumed timer: %v", err)
	}
	if state.record != prior {
		t.Fatal("rollback supplied a new attempt or reset the budget")
	}
}

func TestRecoveryPendingRestartsKeepAuthorizedWindowAndCount(t *testing.T) {
	state, err := openHostRecovery(privateRecoveryTestDirectory(t), "scope", 10, "birth")
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	when, err := state.reserve(time.UnixMilli(100000))
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := state.reserve(time.UnixMilli(100500))
	if err != nil || !resumed.Equal(when) || state.record.Attempts != 1 {
		t.Fatalf("valid cold resume changed pending timestamp/count: %v %+v %v", resumed, state.record, err)
	}
	if err := state.attemptStarted(); err != nil {
		t.Fatal(err)
	}
	next, err := state.reserve(time.UnixMilli(101000))
	if err != nil || next.UnixMilli() != 103000 || state.record.Attempts != 2 {
		t.Fatalf("normal next attempt changed the 2s backoff/count: %v %+v %v", next, state.record, err)
	}
}

func TestRecoveryBackoffDurationUsesValidatedOriginAndElapsedOnly(t *testing.T) {
	now := time.UnixMilli(100000)
	for _, item := range []struct {
		name          string
		whenMS        int64
		elapsed, want time.Duration
	}{
		{"persisted-1s-window", 101000, 100 * time.Millisecond, 900 * time.Millisecond},
		{"persisted-16s-window", 116000, 2 * time.Second, 14 * time.Second},
		{"persistence-consumed-window", 101000, 2 * time.Second, 0},
		{"old-pending-window-elapsed", 99000, 0, 0},
		{"negative-elapsed-cannot-extend-window", 101000, -time.Second, time.Second},
	} {
		t.Run(item.name, func(t *testing.T) {
			if got := recoveryBackoffDelay(time.UnixMilli(item.whenMS), now, item.elapsed); got != item.want {
				t.Fatalf("validated duration=%v want=%v", got, item.want)
			}
		})
	}
}

func TestRecoveryExpiredDurationCannotOverflowIntoFuture(t *testing.T) {
	when := time.UnixMilli(100000)
	now := time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := recoveryBackoffDelay(when, now, time.Millisecond); got != 0 {
		t.Fatalf("expired/saturated duration overflowed into future: %v", got)
	}
}
