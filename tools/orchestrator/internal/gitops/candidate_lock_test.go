package gitops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Done is first observed only after a real failed LOCK_NB and inode capture.
// This synchronizes replacement with the actual lock wait without sleeps.
type observedLockContext struct {
	context.Context
	once    sync.Once
	waiting chan struct{}
}

func (c *observedLockContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestCandidateLockQueuesAndRejectsIdentityDrift(t *testing.T) {
	for _, mode := range []string{"release", "cancel", "lock_replaced", "common_replaced"} {
		t.Run(mode, func(t *testing.T) {
			common := filepath.Join(t.TempDir(), "common")
			if err := os.Mkdir(common, 0700); err != nil {
				t.Fatal(err)
			}
			owner, err := acquireIntegrationLock(common)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { releaseIntegrationLock(owner) }()
			parent, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			ctx := &observedLockContext{Context: parent, waiting: make(chan struct{})}
			type result struct {
				f   *os.File
				err error
			}
			done := make(chan result, 1)
			go func() { f, e := acquireCandidateLock(ctx, common); done <- result{f, e} }()
			select {
			case <-ctx.waiting:
			case r := <-done:
				if r.f != nil {
					releaseIntegrationLock(r.f)
				}
				t.Fatalf("busy lock did not wait: %v", r.err)
			case <-parent.Done():
				t.Fatal("waiter did not reach flock")
			}
			switch mode {
			case "release":
				releaseIntegrationLock(owner)
				owner = nil
			case "cancel":
				cancel()
			case "lock_replaced":
				p := filepath.Join(common, "g3-integration.lock")
				if err = os.Rename(p, p+".old"); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(p, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "common_replaced":
				if err = os.Rename(common, common+".old"); err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(common, 0700); err != nil {
					t.Fatal(err)
				}
			}
			r := <-done
			if r.f != nil {
				releaseIntegrationLock(r.f)
			}
			if mode == "release" {
				if r.err != nil || r.f == nil {
					t.Fatal("released lock not acquired", r.err)
				}
			} else if mode == "cancel" {
				if !errors.Is(r.err, context.Canceled) {
					t.Fatal("wait ignored cancellation", r.err)
				}
			} else if !IsCode(r.err, CodeIdentityChanged) {
				t.Fatal("replacement was not rejected", r.err)
			}
		})
	}
}
