package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
)

type newBasePreparedSubmit struct {
	Version       int    `json:"version"`
	RequestSHA256 string `json:"request_sha256"`
	ControlFile   string `json:"control_file"`
}

func newBasePreparedPath(state string, request coordinator.SubmitRequest) string {
	sum := sha256.Sum256([]byte(request.RunID + "\x00" + request.NewBaseRework.CommandID))
	return filepath.Join(state, "new-base-submissions", hex.EncodeToString(sum[:])+".json")
}
func newBasePublicRequestHash(request coordinator.SubmitRequest, protocol int) string {
	request.NewBaseAuthorization = nil
	request.LaunchID = ""
	request.LaunchToken = ""
	request.ControlToken = ""
	raw, _ := json.Marshal(struct {
		Request  coordinator.SubmitRequest
		Protocol int
	}{request, protocol})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func loadNewBasePrepared(state string, request coordinator.SubmitRequest, protocol int) (string, error) {
	if request.NewBaseRework == nil {
		return "", nil
	}
	path := newBasePreparedPath(state, request)
	if _, e := os.Lstat(path); errors.Is(e, os.ErrNotExist) {
		return "", nil
	} else if e != nil {
		return "", e
	}
	var prepared newBasePreparedSubmit
	if e := readPrivateJSON(path, &prepared); e != nil {
		return "", e
	}
	if prepared.Version != 1 || prepared.RequestSHA256 != newBasePublicRequestHash(request, protocol) || filepath.Dir(prepared.ControlFile) != filepath.Join(state, "control") {
		return "", codeError("new_base_preparation_conflict")
	}
	cap, e := loadControlCapability(prepared.ControlFile)
	if e != nil {
		return "", e
	}
	if cap.RunID != request.RunID || cap.ControllerThread != request.ControllerThread || filepath.Dir(cap.BootstrapPath) != filepath.Join(state, "host-bootstrap") {
		return "", codeError("new_base_preparation_conflict")
	}
	return prepared.ControlFile, nil
}
func publishNewBasePrepared(state string, request coordinator.SubmitRequest, protocol int, control string) (string, error) {
	dir := filepath.Join(state, "new-base-submissions")
	if e := privateSubdir(dir); e != nil {
		return "", e
	}
	suffix, e := randomValue("prepare")
	if e != nil {
		return "", e
	}
	temporary := filepath.Join(dir, "."+suffix+".json")
	if e = writeExclusiveJSON(temporary, newBasePreparedSubmit{Version: 1, RequestSHA256: newBasePublicRequestHash(request, protocol), ControlFile: control}); e != nil {
		return "", e
	}
	defer os.Remove(temporary)
	err := os.Link(temporary, newBasePreparedPath(state, request))
	if err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	f, e := os.Open(dir)
	if e != nil {
		return "", e
	}
	e = f.Sync()
	f.Close()
	if e != nil {
		return "", e
	}

	// Read back the unique winner; a parallel exact submit uses its identities.
	return loadNewBasePrepared(state, request, protocol)
}
