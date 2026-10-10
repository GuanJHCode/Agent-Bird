package process

import (
	"context"
	"os"
	"testing"
)

func TestLiveDescendantTerminalBoundaries(t *testing.T) {
	for _, kind := range []string{"same_uid_baseline", "foreign_uid", "cancelled", "wait_during_last_read", "stop_during_last_read", "zero_binding"} {
		t.Run(kind, func(t *testing.T) {
			uid := os.Geteuid()
			h := &Handle{peerAnchor: &peerAnchor{pid: 10, birth: "original", euid: uid, pgid: 10}}
			h.peerOwner = h
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls, fired := 0, false
			if kind == "foreign_uid" {
				h.peerAnchor.euid++
			}
			if kind == "cancelled" {
				cancel()
			}
			if kind == "zero_binding" {
				if err := new(LiveDescendant).Verify(ctx); err == nil {
					t.Fatal("zero binding granted authority")
				}
				return
			}
			observe := func(context.Context, int) (peerRecord, error) {
				calls++
				if calls == 2 && (kind == "wait_during_last_read" || kind == "stop_during_last_read") {
					h.mu.Lock()
					h.waited = kind == "wait_during_last_read"
					h.peerRevoked = kind == "stop_during_last_read"
					h.mu.Unlock()
					fired = true
				}
				return peerRecord{pid: 10, ppid: 1, birth: "original", euid: uid, pgid: 10}, nil
			}
			err := h.verifyLiveDescendantWith(ctx, 10, observe)
			if kind == "same_uid_baseline" {
				if err != nil || calls != 2 {
					t.Fatalf("valid original leader err=%v reads=%d", err, calls)
				}
			} else if err == nil {
				t.Fatal("invalid original lifetime accepted")
			}
			if (kind == "foreign_uid" || kind == "cancelled") && calls != 0 {
				t.Fatal("rejected input reached kernel observation")
			}
			if (kind == "wait_during_last_read" || kind == "stop_during_last_read") && (!fired || calls != 2) {
				t.Fatal("terminal race oracle did not reach final read")
			}
		})
	}
}
