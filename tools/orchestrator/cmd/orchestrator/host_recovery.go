package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// recoverHost restores only the original source Host. Task transitions remain
// separate owner decisions; this command never calls resume, retry or rework.
func recoverHost(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("recover-host", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "private state directory")
	path := fs.String("request", "", "owner-only recovery request")
	if fs.Parse(args) != nil || *path == "" || fs.NArg() != 0 {
		return codeError("invalid_args")
	}
	var req struct {
		Version         int    `json:"version"`
		TaskID          string `json:"task_id"`
		WorkRevision    int    `json:"work_revision"`
		ControlFile     string `json:"control_file"`
		OwnerCapability string `json:"owner_capability"`
		HostSHA256      string `json:"host_sha256"`
	}
	if err := readPrivateJSON(*path, &req); err != nil {
		return err
	}
	digest, err := hex.DecodeString(req.HostSHA256)
	if req.Version != 1 || req.TaskID == "" || req.WorkRevision < 1 || req.OwnerCapability == "" || err != nil || len(digest) != 32 {
		return codeError("invalid_host_recovery")
	}
	capability, err := loadControlCapability(req.ControlFile)
	if err != nil {
		return err
	}
	if capability.Version != 2 {
		return codeError("legacy_host_recovery_unsupported")
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	// Cover acquisition, inspection and startup with one bounded caller window.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	lease, err := openHostLaunchLease(ctx, state, capability)
	if err != nil {
		return err
	}
	defer lease.close()
	b, err := lease.read(capability)
	if err != nil {
		return err
	}
	query := coordinator.HostRecoveryRequest{TaskControlRequest: coordinator.TaskControlRequest{TaskID: req.TaskID, ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken, WorkRevision: req.WorkRevision}, OwnerCapability: req.OwnerCapability}
	inspect := func() (store.HostRecovery, error) {
		var r store.HostRecovery
		if err := lease.unchanged(); err != nil {
			return r, err
		}
		response, err := call(ctx, state, ipc.KindRecoverHost, query)
		if err != nil {
			return r, err
		}
		if err = decode(response.Payload, &r); err != nil {
			return r, err
		}
		if r.RunID != capability.RunID || r.LaunchID != b.Hello.LaunchID || r.OriginContextID != b.Hello.OriginContextID || r.OriginPID != b.Hello.OriginPID || r.OriginBirth != b.Hello.OriginBirth || r.HostGeneration != b.Hello.HostGeneration || r.Executable != b.Hello.Executable {
			return r, codeError("host_recovery_binding_mismatch")
		}
		return r, lease.unchanged()
	}
	r, err := inspect()
	if err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(r.Executable)
	if err != nil || !filepath.IsAbs(r.Executable) || canonical != r.Executable {
		return codeError("host_recovery_executable_mismatch")
	}
	actual, err := process.ExecutableDigest(ctx, r.Executable)
	if err != nil || actual != hex.EncodeToString(digest) {
		return codeError("host_recovery_executable_mismatch")
	}
	if r.Status == "ready" {
		if err = lease.unchanged(); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]any{"version": 1, "status": "host_ready", "run_id": r.RunID, "host_pid": r.PID, "host_birth": r.Birth})
	}
	// One startup intent per launch is retained even if the caller crashes before
	// observing HostReady. A retry may observe a ready Host, but never spawn again
	// from an unresolved intent. Recovery does not delete evidence to unlock it.
	if err = lease.writeRecoveryIntent(map[string]any{"version": 1, "launch_id": r.LaunchID, "host_generation": r.HostGeneration, "host_sha256": actual}); errors.Is(err, os.ErrExist) {
		return codeError("host_recovery_uncertain")
	} else if err != nil {
		return err
	}
	if err = startSourceHostContext(ctx, r.Executable, state, b); err != nil {
		return codeError("host_recovery_uncertain")
	}
	if err = waitForHost(ctx, state, b.Hello.LaunchID, b.Hello.LaunchToken); err != nil {
		return codeError("host_recovery_uncertain")
	}
	r, err = inspect()
	if err != nil || r.Status != "ready" {
		return codeError("host_recovery_uncertain")
	}
	return json.NewEncoder(out).Encode(map[string]any{"version": 1, "status": "host_ready", "run_id": r.RunID, "host_pid": r.PID, "host_birth": r.Birth})
}
