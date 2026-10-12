package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func hostRetentionCLI(ctx context.Context, action string, args []string, out io.Writer) (err error) {
	defer func() {
		if errors.Is(err, context.Canceled) {
			err = codeError("host_retention_cancelled")
		} else if errors.Is(err, context.DeadlineExceeded) {
			err = codeError("host_retention_timeout")
		} else {
			err = diagnosticsCLIError(err)
		}
	}()
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "private state directory")
	controlFile := fs.String("control-file", "", "original run control capability")
	requestFile := fs.String("request", "", "owner-only host retention request or saved plan")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *controlFile == "" || *requestFile == "" {
		return codeError("invalid_args")
	}
	raw, err := readDiagnosticsRequestFile(*requestFile)
	if err != nil {
		return codeError("host_retention_request_file_invalid")
	}
	var request any
	var runID string
	var kind ipc.Kind
	switch action {
	case "host-artifact-retention-resolve":
		var r store.HostArtifactResolutionRequest
		if json.Unmarshal(raw, &r) != nil {
			return codeError("host_retention_request_invalid")
		}
		request, runID, kind = r, r.RunID, ipc.KindHostArtifactResolve
	case "host-artifact-retention-plan":
		var r store.HostArtifactRetentionPlanRequest
		if json.Unmarshal(raw, &r) != nil {
			return codeError("host_retention_request_invalid")
		}
		request, runID, kind = r, r.RunID, ipc.KindHostArtifactRetentionPlan
	case "host-artifact-retention-apply":
		var r store.HostArtifactRetentionPlan
		if json.Unmarshal(raw, &r) != nil {
			return codeError("host_retention_plan_invalid")
		}
		request, runID, kind = r, r.RunID, ipc.KindHostArtifactRetentionApply
	case "host-gc-enable", "host-gc-disable", "host-gc-status":
		var r store.HostGCRequest
		if json.Unmarshal(raw, &r) != nil {
			return codeError("host_retention_request_invalid")
		}
		request, runID, kind = r, r.RunID, ipc.KindHostGCStatus
		if action == "host-gc-enable" {
			kind = ipc.KindHostGCEnable
		}
		if action == "host-gc-disable" {
			kind = ipc.KindHostGCDisable
		}
	default:
		return codeError("invalid_args")
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	capability, err := loadControlCapability(*controlFile)
	if err != nil {
		return err
	}
	if capability.RunID != runID {
		return codeError("host_retention_scope_mismatch")
	}
	payload := coordinator.HostRetentionRequest[any]{ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken, Request: request}
	response, err := call(ctx, state, kind, payload)
	if err != nil {
		return err
	}
	_, err = out.Write(append(response.Payload, '\n'))
	return err
}
