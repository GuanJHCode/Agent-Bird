package store

import (
	"context"
	"time"
)

// bridgeLockContext uses the existing DB mutex. A cancelled caller never
// leaves a goroutine waiting to acquire it, and cannot gain the lock later.
func (d *DB) bridgeLockContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		if d.mu.TryLock() {
			if err := ctx.Err(); err != nil {
				d.mu.Unlock()
				return err
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
