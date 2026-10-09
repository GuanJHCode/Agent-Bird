package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Candidate providers can run in parallel. Queue only their Git critical
// section; a busy advisory lock is not evidence that the candidate drifted.
// Retain one original descriptor throughout the wait, and never move onto a
// replacement lock inode. Final integration keeps its separate fail-fast API.
func acquireCandidateLock(ctx context.Context, common string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity, err := directoryIdentity(common)
	if err != nil {
		return nil, err
	}
	f, err := openIntegrationLock(common)
	if err != nil {
		return nil, err
	}
	acquired := false
	defer func() {
		if !acquired {
			_ = f.Close()
		}
	}()
	held, err := f.Stat()
	if err != nil {
		return nil, err
	}
	verify := func() error {
		if latest, e := directoryIdentity(common); e != nil || !sameDirectoryIdentity(latest, identity) {
			return fail(CodeIdentityChanged, "candidate lock", fmt.Errorf("common directory changed"))
		}
		named, e := os.Lstat(filepath.Join(common, "g3-integration.lock"))
		if e != nil || !named.Mode().IsRegular() || !os.SameFile(named, held) {
			return fail(CodeIdentityChanged, "candidate lock", fmt.Errorf("lock inode changed"))
		}
		stat, ok := named.Sys().(*syscall.Stat_t)
		if !ok || stat.Nlink != 1 || int64(stat.Uid) != int64(os.Getuid()) || named.Mode().Perm() != 0600 {
			return fail(CodeIdentityChanged, "candidate lock", fmt.Errorf("lock ownership changed"))
		}
		return nil
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if err = verify(); err != nil {
			return nil, err
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			if err = verify(); err != nil {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				return nil, err
			}
			if err = ctx.Err(); err != nil {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				return nil, err
			}
			acquired = true
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return nil, fail(CodeTargetDrift, "candidate lock", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fail(CodeTargetDrift, "candidate lock", fmt.Errorf("candidate lock wait timed out"))
		case <-tick.C:
		}
	}
}
