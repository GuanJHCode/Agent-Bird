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
	return loadNewBasePreparedWithSync(state, request, protocol, syncNewBaseDirectory)
}
func loadNewBasePreparedWithSync(state string, request coordinator.SubmitRequest, protocol int, syncDir func(string) error) (string, error) {
	control, err := readNewBasePrepared(state, request, protocol)
	if err != nil || control == "" {
		return control, err
	}
	// A previous Link may have succeeded before marker-directory Sync failed.
	// Retry the full barrier before exposing the same identities to Submit.
	if err = syncNewBaseIdentityDirectories(state, syncDir); err != nil {
		return "", err
	}
	if err = syncDir(filepath.Join(state, "new-base-submissions")); err != nil {
		return "", err
	}
	return control, nil
}
func readNewBasePrepared(state string, request coordinator.SubmitRequest, protocol int) (string, error) {
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
	return publishNewBasePreparedWithSync(state, request, protocol, control, syncNewBaseDirectory)
}
func publishNewBasePreparedWithSync(state string, request coordinator.SubmitRequest, protocol int, control string, syncDir func(string) error) (string, error) {
	dir := filepath.Join(state, "new-base-submissions")
	if e := privateSubdir(dir); e != nil {
		return "", e
	}
	// Bootstrap/control files are already file-Synced. Persist their directory
	// entries and all newly created state children before publishing the marker.
	if e := syncNewBaseIdentityDirectories(state, syncDir); e != nil {
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
	if e = syncDir(dir); e != nil {
		return "", e
	}
	// Read back the unique winner; a parallel exact submit uses its identities.
	return readNewBasePrepared(state, request, protocol)
}
func syncNewBaseIdentityDirectories(state string, syncDir func(string) error) error {
	for _, dir := range []string{filepath.Join(state, "host-bootstrap"), filepath.Join(state, "control"), state} {
		if err := syncDir(dir); err != nil {
			return err
		}
	}
	return nil
}
func syncNewBaseDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 || !owned(info) {
		return codeError("untrusted_private_dir")
	}
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = f.Sync()
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}
