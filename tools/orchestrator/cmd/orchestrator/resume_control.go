package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type resumeControlDependencies struct {
	requireCoordinator  func(context.Context, string) error
	call                func(context.Context, string, ipc.Kind, any) (ipc.Envelope, error)
	ensureLegacyHost    func(context.Context, string, controlCapability) error
	ensureSuccessorHost func(context.Context, string, controlCapability, coordinator.ResumeResponse) error
}

func validateResumeReceipt(raw []byte, request coordinator.ResumeRequest, capability controlCapability) (coordinator.ResumeResponse, error) {
	var response coordinator.ResumeResponse
	invalid := codeError("resume_receipt_invalid")
	if len(raw) > 64<<10 || contract.DecodeBridgeStrict(raw, &response) != nil {
		return coordinator.ResumeResponse{}, invalid
	}
	required := []string{"version", "status", "mode", "command_id", "request_sha256", "run_id", "task_id", "launch_id", "owner_id", "work_revision", "control_revision", "coordinator_epoch", "ready_sequence", "created_at"}
	switch response.Mode {
	case "existing_owner":
	case "successor":
		required = append(required, "authorization_id")
		if response.AuthorizationID == "" {
			return coordinator.ResumeResponse{}, invalid
		}
	default:
		return coordinator.ResumeResponse{}, invalid
	}
	if !resumeResponseFields(raw, required) {
		return coordinator.ResumeResponse{}, invalid
	}
	hash, err := store.OriginalOwnerResumeRequestSHA256(store.OriginalOwnerResumeRequest{TaskID: request.TaskID, ControllerThread: request.ControllerThread, ControlToken: request.ControlToken, WorkRevision: request.WorkRevision, CommandID: request.CommandID})
	created, timeErr := time.Parse(time.RFC3339Nano, response.CreatedAt)
	if err != nil || response.Version != 1 || response.Status != "resume_queued" || response.RequestSHA256 != hash {
		return coordinator.ResumeResponse{}, invalid
	}
	if response.CommandID != request.CommandID || response.TaskID != request.TaskID || response.WorkRevision != request.WorkRevision || capability.RunID == "" || response.RunID != capability.RunID || response.LaunchID == "" || (capability.HostLaunchID != "" && response.LaunchID != capability.HostLaunchID) {
		return coordinator.ResumeResponse{}, invalid
	}
	if response.OwnerID == "" || response.ControlRevision == 0 || response.CoordinatorEpoch == 0 || response.ReadySequence <= 0 || timeErr != nil || created.IsZero() || created.UTC().Format(time.RFC3339Nano) != response.CreatedAt {
		return coordinator.ResumeResponse{}, invalid
	}
	return response, nil
}

func resumeResponseFields(raw []byte, required []string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != len(required) {
		return false
	}
	for _, name := range required {
		value, found := fields[name]
		if !found || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	return true
}

func resumeControl(ctx context.Context, state, controlFile, taskID string, revision int, commandID string, explicit bool, out io.Writer, deps resumeControlDependencies) error {
	if taskID == "" || revision < 1 || (explicit && !validResumeJournalCommandID(commandID)) || (!explicit && commandID != "") {
		return codeError("invalid_args")
	}
	// Authenticate the private reference before creating even a local command.
	capability, err := loadControlCapability(controlFile)
	if err != nil {
		return err
	}
	if err = deps.requireCoordinator(ctx, state); err != nil {
		return err
	}
	journal, err := selectResumeJournalRequest(state, controlFile, taskID, revision, commandID)
	if err != nil {
		return err
	}
	request := coordinator.ResumeRequest{TaskControlRequest: coordinator.TaskControlRequest{TaskID: taskID, ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken, WorkRevision: revision}, CommandID: journal.CommandID}
	response, err := deps.call(ctx, state, ipc.KindResume, request)
	if err != nil {
		var remote remoteError
		if explicit || !errors.As(err, &remote) || remote != remoteError("resume_command_not_supported") {
			return err
		}
		// The authenticated legacy route lacks command receipts. Preserve its
		// original guarded ordering without claiming stable remote replay.
		response, err = legacyResumeControl(ctx, state, capability, request, deps)
		if err != nil {
			return err
		}
	} else {
		receipt, validateErr := validateResumeReceipt(response.Payload, request, capability)
		if validateErr != nil {
			return validateErr
		}
		if receipt.Mode == "successor" {
			if err = deps.ensureSuccessorHost(ctx, state, capability, receipt); err != nil {
				return err
			}
		}
	}
	body := append(response.Payload, '\n')
	n, err := out.Write(body)
	if err != nil {
		return err
	}
	if n != len(body) {
		return io.ErrShortWrite
	}
	return completeResumeJournalRequest(state, journal)
}

func legacyResumeControl(ctx context.Context, state string, capability controlCapability, request coordinator.ResumeRequest, deps resumeControlDependencies) (ipc.Envelope, error) {
	response, err := deps.call(ctx, state, ipc.KindSummary, coordinator.TaskControlRequest{TaskID: request.TaskID, ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken})
	if err != nil {
		return ipc.Envelope{}, err
	}
	var summary store.RunSummary
	if err = decode(response.Payload, &summary); err != nil {
		return ipc.Envelope{}, err
	}
	for _, task := range summary.Tasks {
		if task.TaskID == request.TaskID && task.ResumeBlockedReason != "" {
			return ipc.Envelope{}, codeError(task.ResumeBlockedReason)
		}
	}
	if err = deps.ensureLegacyHost(ctx, state, capability); err != nil {
		return ipc.Envelope{}, err
	}
	request.CommandID = ""
	response, err = deps.call(ctx, state, ipc.KindResume, request)
	if err != nil {
		return ipc.Envelope{}, err
	}
	var receipt coordinator.StatusResponse
	if len(response.Payload) > 64<<10 || contract.DecodeBridgeStrict(response.Payload, &receipt) != nil || !resumeResponseFields(response.Payload, []string{"status"}) || receipt.Status != "resume_queued" {
		return ipc.Envelope{}, codeError("resume_receipt_invalid")
	}
	return response, nil
}
