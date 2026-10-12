package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
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

func acpPromptReservationName(begin contract.ACPBeginRequest) string {
	key, _ := json.Marshal([]any{"acp-prompt-reservation-v1", begin.BindingID, begin.OwnerEpoch, begin.NativeSessionID, begin.BatchID, begin.AttemptID, begin.RenderSHA256, begin.PromptSHA256, begin.TurnOrdinal})
	sum := sha256.Sum256(key)
	return "reservation-" + hex.EncodeToString(sum[:]) + ".json"
}

func admitACPCompletion(parent *os.Root, incoming int64, replacing string) error {
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
		if name == acpCompletionQuotaLock || name == replacing {
			continue
		}
		isReceipt := len(name) == len("receipt-")+64+len(".json") && strings.HasPrefix(name, "receipt-") && strings.HasSuffix(name, ".json")
		isReservation := len(name) == len("reservation-")+64+len(".json") && strings.HasPrefix(name, "reservation-") && strings.HasSuffix(name, ".json")
		if !isReceipt && !isReservation {
			return invalid
		}
		prefix := "receipt-"
		if isReservation {
			prefix = "reservation-"
		}
		digest := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".json")
		decoded, err := hex.DecodeString(digest)
		if err != nil || hex.EncodeToString(decoded) != digest {
			return invalid
		}
		info, err := parent.Lstat(name)
		if err != nil || !acpPrivateReceipt(info) {
			return invalid
		}
		if isReservation {
			if err := validateACPPromptReservation(parent, name); err != nil {
				return invalid
			}
			used += bridgeMaxRequestFile
		} else {
			used += info.Size()
		}
		count++
		if count >= acpCompletionMaximumFiles || used > acpCompletionMaximumBytes-incoming {
			return capacity
		}
	}
	return nil
}
