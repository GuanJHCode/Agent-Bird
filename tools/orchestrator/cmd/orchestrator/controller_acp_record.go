package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

const acpCompletionDirectory = "acp-origin-completions"

func acpPrivateDirectory(info os.FileInfo) bool {
	return info != nil && info.Mode() == os.ModeDir|0700 && owned(info)
}

func acpPrivateReceipt(info os.FileInfo) bool {
	if info == nil || info.Mode() != 0600 || info.Size() < 1 || info.Size() > bridgeMaxRequestFile || !owned(info) {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink == 1
}

func syncACPDirectory(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// persistACPCompletion stores the complete private Receive tuple before its
// Store RPC. One binding/epoch/attempt has exactly one immutable local record;
// an exact replay only resyncs it. A partial/foreign/conflicting file is never
// repaired or replaced, and a filesystem error never authorizes another Prompt.
func persistACPCompletion(state string, req contract.ACPReceiveRequest) (string, error) {
	invalid := codeError("controller_acp_completion_invalid")
	failed := codeError("controller_acp_completion_write_failed")
	raw, err := json.Marshal(req)
	if err != nil {
		return "", invalid
	}
	if _, err = contract.DecodeACPReceiveRequest(raw); err != nil {
		return "", invalid
	}
	if !filepath.IsAbs(state) || filepath.Clean(state) != state {
		return "", invalid
	}
	canonical, err := filepath.EvalSymlinks(state)
	stateInfo, e := os.Lstat(state)
	if err != nil || e != nil || canonical != state || !acpPrivateDirectory(stateInfo) {
		return "", invalid
	}
	stateRoot, err := os.OpenRoot(state)
	if err != nil {
		return "", invalid
	}
	defer stateRoot.Close()
	held, e := stateRoot.Stat(".")
	if e != nil || !os.SameFile(stateInfo, held) || !acpPrivateDirectory(held) {
		return "", invalid
	}
	if err = stateRoot.Mkdir(acpCompletionDirectory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", failed
	}
	parentInfo, err := stateRoot.Lstat(acpCompletionDirectory)
	if err != nil || !acpPrivateDirectory(parentInfo) {
		return "", invalid
	}
	parent, err := stateRoot.OpenRoot(acpCompletionDirectory)
	if err != nil {
		return "", invalid
	}
	defer parent.Close()
	parentHeld, err := parent.Stat(".")
	if err != nil || !os.SameFile(parentInfo, parentHeld) || !acpPrivateDirectory(parentHeld) {
		return "", invalid
	}
	checkNamed := func() bool {
		current, e1 := os.Lstat(state)
		canonical, e2 := filepath.EvalSymlinks(state)
		named, e3 := stateRoot.Lstat(acpCompletionDirectory)
		return e1 == nil && e2 == nil && e3 == nil && canonical == state && acpPrivateDirectory(current) && acpPrivateDirectory(named) && os.SameFile(stateInfo, current) && os.SameFile(parentInfo, named)
	}
	if !checkNamed() {
		return "", invalid
	}
	key, _ := json.Marshal([]any{"acp-local-completion-v1", req.BindingID, req.OwnerEpoch, req.AttemptID})
	sum := sha256.Sum256(key)
	name := "receipt-" + hex.EncodeToString(sum[:]) + ".json"
	path := filepath.Join(state, acpCompletionDirectory, name)
	lock, err := lockACPCompletions(parent)
	if err != nil {
		return "", err
	}
	defer lock.Close() // Closing the original descriptor releases flock.
	if !checkNamed() || !acpCompletionLockValid(parent, lock) {
		return "", invalid
	}
	// Replays remain available even when an old directory is already at quota.
	// Only a new immutable receipt consumes one entry and its complete bytes.
	reservation := acpPromptReservationName(req.ACPBeginRequest)
	if held, e := parent.Lstat(reservation); e == nil {
		if !acpPrivateReceipt(held) || validateACPPromptReservation(parent, reservation) != nil {
			return "", invalid
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return "", invalid
	}
	if _, err = parent.Lstat(name); errors.Is(err, os.ErrNotExist) {
		if err = admitACPCompletion(parent, int64(len(raw)+1), reservation); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", invalid
	}
	file, err := parent.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		before, e := parent.Lstat(name)
		if e != nil || !acpPrivateReceipt(before) {
			return "", invalid
		}
		file, e = parent.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
		if e != nil {
			return "", invalid
		}
		defer file.Close()
		info, e := file.Stat()
		if e != nil || !acpPrivateReceipt(info) || !os.SameFile(before, info) {
			return "", invalid
		}
		body, e := io.ReadAll(io.LimitReader(file, bridgeMaxRequestFile+1))
		if e != nil || len(body) > bridgeMaxRequestFile {
			return "", invalid
		}
		current, e1 := file.Stat()
		named, e2 := parent.Lstat(name)
		if e1 != nil || e2 != nil || !acpPrivateReceipt(current) || !acpPrivateReceipt(named) || !os.SameFile(info, current) || !os.SameFile(current, named) || current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) {
			return "", invalid
		}
		stored, e := contract.DecodeACPReceiveRequest(body)
		if e != nil {
			return "", invalid
		}
		if stored != req {
			return "", codeError("controller_acp_completion_conflict")
		}
		if file.Sync() != nil {
			return "", failed
		}
	} else if err != nil {
		return "", failed
	} else {
		info, e := file.Stat()
		if e != nil || info.Mode() != 0600 || !owned(info) {
			file.Close()
			return "", invalid
		}
		payload := append(raw, '\n')
		n, e := file.Write(payload)
		if e == nil && n != len(payload) {
			e = io.ErrShortWrite
		}
		if e == nil {
			e = file.Sync()
		}
		closeErr := file.Close()
		if e != nil || closeErr != nil {
			return "", failed
		}
		named, e := parent.Lstat(name)
		if e != nil || !acpPrivateReceipt(named) || !os.SameFile(info, named) {
			return "", invalid
		}
	}
	// A reservation is retained through an UNKNOWN Prompt outcome. It is only
	// removed after the complete immutable receipt has been synced.
	if held, e := parent.Lstat(reservation); e == nil {
		if !acpPrivateReceipt(held) || validateACPPromptReservation(parent, reservation) != nil || parent.Remove(reservation) != nil {
			return "", invalid
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return "", invalid
	}
	// Include the new directory entry and, on first creation, its state parent.
	if !checkNamed() || !acpCompletionLockValid(parent, lock) {
		return "", invalid
	}
	if syncACPDirectory(parent) != nil || syncACPDirectory(stateRoot) != nil {
		return "", failed
	}
	if !checkNamed() || !acpCompletionLockValid(parent, lock) {
		return "", invalid
	}
	return path, nil
}
