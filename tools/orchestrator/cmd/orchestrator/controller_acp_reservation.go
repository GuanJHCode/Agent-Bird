package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// acpPromptReservation deliberately contains only public protocol bindings and
// hashes. In particular it must never retain the claim token or prompt text.
type acpPromptReservation struct {
	Version         int    `json:"version"`
	BindingID       string `json:"binding_id"`
	OwnerEpoch      uint64 `json:"owner_epoch"`
	NativeSessionID string `json:"native_session_id"`
	BatchID         string `json:"batch_id"`
	AttemptID       string `json:"attempt_id"`
	RenderSHA256    string `json:"render_sha256"`
	PromptSHA256    string `json:"prompt_sha256"`
	TurnOrdinal     int64  `json:"turn_ordinal"`
}

func acpReservationFor(begin contract.ACPBeginRequest) acpPromptReservation {
	return acpPromptReservation{1, begin.BindingID, begin.OwnerEpoch, begin.NativeSessionID, begin.BatchID, begin.AttemptID, begin.RenderSHA256, begin.PromptSHA256, begin.TurnOrdinal}
}

func (r acpPromptReservation) name() string {
	return acpPromptReservationName(contract.ACPBeginRequest{BridgeOwnerRequest: contract.BridgeOwnerRequest{BridgeRequestBase: contract.BridgeRequestBase{BindingID: r.BindingID, OwnerEpoch: r.OwnerEpoch}}, NativeSessionID: r.NativeSessionID, BatchID: r.BatchID, AttemptID: r.AttemptID, RenderSHA256: r.RenderSHA256, PromptSHA256: r.PromptSHA256, TurnOrdinal: r.TurnOrdinal})
}

func decodeACPPromptReservation(raw []byte) (acpPromptReservation, error) {
	var r acpPromptReservation
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil || decoder.Decode(&struct{}{}) != io.EOF || r.Version != 1 || r.BindingID == "" || r.OwnerEpoch == 0 || r.NativeSessionID == "" || r.BatchID == "" || r.AttemptID == "" || len(r.RenderSHA256) != 64 || len(r.PromptSHA256) != 64 || r.TurnOrdinal < 1 {
		return acpPromptReservation{}, errors.New("invalid reservation")
	}
	return r, nil
}

func validateACPPromptReservation(parent *os.Root, name string) error {
	f, err := parent.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !acpPrivateReceipt(info) {
		return errors.New("invalid reservation")
	}
	raw, err := io.ReadAll(io.LimitReader(f, bridgeMaxRequestFile+1))
	if err != nil || len(raw) > bridgeMaxRequestFile {
		return errors.New("invalid reservation")
	}
	r, err := decodeACPPromptReservation(raw)
	if err != nil || r.name() != name {
		return errors.New("invalid reservation")
	}
	named, err := parent.Lstat(name)
	if err != nil || !acpPrivateReceipt(named) || !os.SameFile(info, named) {
		return errors.New("invalid reservation")
	}
	return nil
}

// reserveACPPrompt reserves a full request-file budget before the remote write.
// An already present record is UNKNOWN after a prior owner died, so it never
// authorizes a replayed Prompt.
func reserveACPPrompt(state string, begin contract.ACPBeginRequest) (string, error) {
	invalid := codeError("controller_acp_completion_invalid")
	rawBegin, _ := json.Marshal(begin)
	if _, err := contract.DecodeACPBeginRequest(rawBegin); err != nil {
		return "", invalid
	}
	raw, err := json.Marshal(acpReservationFor(begin))
	if err != nil || len(raw) == 0 || len(raw) > bridgeMaxRequestFile {
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
	stateHeld, err := stateRoot.Stat(".")
	if err != nil || !os.SameFile(stateInfo, stateHeld) || !acpPrivateDirectory(stateHeld) {
		return "", invalid
	}
	if err = stateRoot.Mkdir(acpCompletionDirectory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", codeError("controller_acp_completion_write_failed")
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
	name := acpPromptReservationName(begin)
	lock, err := lockACPCompletions(parent)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if !checkNamed() || !acpCompletionLockValid(parent, lock) {
		return "", invalid
	}
	if _, err = parent.Lstat(name); err == nil {
		if validateACPPromptReservation(parent, name) != nil {
			return "", invalid
		}
		return "", codeError("controller_acp_prompt_unknown")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", invalid
	}
	if err = admitACPCompletion(parent, bridgeMaxRequestFile, ""); err != nil {
		return "", err
	}
	f, err := parent.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", codeError("controller_acp_completion_write_failed")
	}
	if n, e := f.Write(append(raw, '\n')); e != nil || n != len(raw)+1 || f.Sync() != nil || f.Close() != nil {
		f.Close()
		return "", codeError("controller_acp_completion_write_failed")
	}
	if !checkNamed() || !acpCompletionLockValid(parent, lock) || validateACPPromptReservation(parent, name) != nil || syncACPDirectory(parent) != nil || syncACPDirectory(stateRoot) != nil {
		return "", codeError("controller_acp_completion_write_failed")
	}
	return filepath.Join(state, acpCompletionDirectory, name), nil
}
