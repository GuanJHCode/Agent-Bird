package admincli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"codex-cli-orchestration-design/tools/orchestrator/internal/install"
)

type InstallRequest struct {
	SourceRoot      string `json:"source_root"`
	BinaryPath      string `json:"binary_path"`
	DestinationRoot string `json:"destination_root"`
	DataRoot        string `json:"data_root"`
	Version         string `json:"version"`
}

type VersionRequest struct {
	DestinationRoot string `json:"destination_root"`
	Version         string `json:"version"`
}

type DoctorRequest struct {
	DestinationRoot string `json:"destination_root"`
}

type PinRequest struct {
	DestinationRoot string `json:"destination_root"`
	TaskID          string `json:"task_id"`
	Version         string `json:"version"`
}

type UnpinRequest struct {
	DestinationRoot string `json:"destination_root"`
	TaskID          string `json:"task_id"`
}

func RunInstall(request InstallRequest) (install.InstallResult, error) {
	return install.InstallPackage(install.InstallOptions{SourceRoot: request.SourceRoot, BinaryPath: request.BinaryPath, DestinationRoot: request.DestinationRoot, DataRoot: request.DataRoot, Version: request.Version})
}

func RunDoctor(request DoctorRequest) (install.DoctorReport, error) {
	return install.Doctor(request.DestinationRoot)
}

func RunUninstall(request VersionRequest) (install.UninstallResult, error) {
	return install.UninstallPackage(request.DestinationRoot, request.Version)
}

func RunPin(request PinRequest) error {
	return install.PinVersion(request.DestinationRoot, request.TaskID, request.Version)
}

func RunUnpin(request UnpinRequest) error {
	return install.UnpinVersion(request.DestinationRoot, request.TaskID)
}

// Run is the single request-file bridge used by cmd/orchestrator's thin admin
// switches. Request contents never appear in argv, and every result is JSON.
func Run(operation, requestPath string, stdout io.Writer) error {
	if stdout == nil {
		return errors.New("admin_stdout_required")
	}
	var result any
	var err error
	switch operation {
	case "install":
		var request InstallRequest
		if err = readRequest(requestPath, &request); err == nil {
			result, err = RunInstall(request)
		}
	case "doctor":
		var request DoctorRequest
		if err = readRequest(requestPath, &request); err == nil {
			result, err = RunDoctor(request)
		}
	case "uninstall":
		var request VersionRequest
		if err = readRequest(requestPath, &request); err == nil {
			result, err = RunUninstall(request)
		}
	case "pin":
		var request PinRequest
		if err = readRequest(requestPath, &request); err == nil {
			err = RunPin(request)
			result = map[string]string{"task_id": request.TaskID, "version": request.Version}
		}
	case "unpin":
		var request UnpinRequest
		if err = readRequest(requestPath, &request); err == nil {
			err = RunUnpin(request)
			result = map[string]string{"task_id": request.TaskID}
		}
	default:
		return errors.New("admin_operation_invalid")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(map[string]any{"version": 1, "status": "ok", "operation": operation, "result": result})
}

func readRequest(path string, target any) error {
	if !filepath.IsAbs(path) {
		return errors.New("admin_request_path_invalid")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return errors.New("admin_request_unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("admin_request_unsafe")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return errors.New("admin_request_invalid")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("admin_request_invalid")
	}
	return nil
}
