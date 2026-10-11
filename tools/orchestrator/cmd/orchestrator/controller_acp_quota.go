package main

import (
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
)

const (
	acpCompletionQuotaLock          = ".quota.lock"
	acpCompletionMaximumFiles       = 1024
	acpCompletionMaximumBytes int64 = 16 << 20
)

func acpCompletionLockValid(parent *os.Root, lock *os.File) bool {
	held, e1 := lock.Stat()
	named, e2 := parent.Lstat(acpCompletionQuotaLock)
	if e1 != nil || e2 != nil || !os.SameFile(held, named) || held.Mode() != 0600 || named.Mode() != 0600 || held.Size() != 0 || named.Size() != 0 || !owned(held) || !owned(named) {
		return false
	}
	h, ok := held.Sys().(*syscall.Stat_t)
	n, nok := named.Sys().(*syscall.Stat_t)
	return ok && nok && h.Nlink == 1 && n.Nlink == 1
}

// The permanent empty lock coordinates cooperating processes. It is never
// removed, so an open old inode cannot become a second concurrent lock domain.
func lockACPCompletions(parent *os.Root) (*os.File, error) {
	f, err := parent.OpenFile(acpCompletionQuotaLock, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, codeError("controller_acp_completion_invalid")
	}
	if !acpCompletionLockValid(parent, f) {
		f.Close()
		return nil, codeError("controller_acp_completion_invalid")
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, codeError("controller_acp_completion_busy")
		}
		return nil, codeError("controller_acp_completion_write_failed")
	}
	if !acpCompletionLockValid(parent, f) {
		f.Close()
		return nil, codeError("controller_acp_completion_invalid")
	}
	return f, nil
}

func admitACPCompletion(parent *os.Root, incoming int64) error {
	invalid := codeError("controller_acp_completion_invalid")
	capacity := codeError("controller_acp_completion_capacity_exhausted")
	if incoming <= 0 || incoming > bridgeMaxRequestFile {
		return invalid
	}
	dir, err := parent.Open(".")
	if err != nil {
		return invalid
	}
	entries, err := dir.ReadDir(acpCompletionMaximumFiles + 2)
	closeErr := dir.Close()
	if err != nil && err != io.EOF || closeErr != nil {
		return invalid
	}
	count, used := 0, int64(0)
	for _, entry := range entries {
		name := entry.Name()
		if name == acpCompletionQuotaLock {
			continue
		}
		if len(name) != len("receipt-")+64+len(".json") || !strings.HasPrefix(name, "receipt-") || !strings.HasSuffix(name, ".json") {
			return invalid
		}
		digest := strings.TrimSuffix(strings.TrimPrefix(name, "receipt-"), ".json")
		decoded, err := hex.DecodeString(digest)
		if err != nil || hex.EncodeToString(decoded) != digest {
			return invalid
		}
		info, err := parent.Lstat(name)
		if err != nil || !acpPrivateReceipt(info) {
			return invalid
		}
		count++
		used += info.Size()
		if count >= acpCompletionMaximumFiles || used > acpCompletionMaximumBytes-incoming {
			return capacity
		}
	}
	return nil
}
