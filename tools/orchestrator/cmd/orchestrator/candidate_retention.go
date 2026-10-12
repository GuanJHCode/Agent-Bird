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

func candidateRetentionCLI(ctx context.Context, action string, args []string, out io.Writer) (err error) {
	defer func() {
		if errors.Is(err, context.Canceled) {
			err = codeError("candidate_retention_cancelled")
		} else if errors.Is(err, context.DeadlineExceeded) {
			err = codeError("candidate_retention_timeout")
		} else {
			err = diagnosticsCLIError(err)
		}
	}()
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "private state directory")
	controlFile := fs.String("control-file", "", "original run control capability")
	requestFile := fs.String("request", "", "owner-only candidate retention request or saved plan")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *controlFile == "" || *requestFile == "" {
		return codeError("invalid_args")
	}
	raw, err := readDiagnosticsRequestFile(*requestFile)
	if err != nil {
		return codeError("candidate_retention_request_file_invalid")
	}
	var request any
	var runID string
	var kind ipc.Kind
	switch action {
	case "candidate-retention-resolve":
		var r store.CandidateRetentionResolutionRequest
		if json.Unmarshal(raw, &r) != nil {
			return codeError("candidate_retention_request_invalid")
		}
		request, runID, kind = r, r.RunID, ipc.KindCandidateRetentionResolve
	case "candidate-retention-plan":
		var r store.CandidateRetentionPlanRequest
		if json.Unmarshal(raw, &r) != nil {
			return codeError("candidate_retention_request_invalid")
		}
		request, runID, kind = r, r.RunID, ipc.KindCandidateRetentionPlan
	case "candidate-retention-apply":
		var r store.CandidateRetentionPlan
		if json.Unmarshal(raw, &r) != nil {
			return codeError("candidate_retention_plan_invalid")
		}
		request, runID, kind = r, r.RunID, ipc.KindCandidateRetentionApply
	case "candidate-gc-enable", "candidate-gc-disable", "candidate-gc-status":
		var r store.CandidateGCRequest
		if json.Unmarshal(raw, &r) != nil {
			return codeError("candidate_retention_request_invalid")
		}
		request, runID, kind = r, r.RunID, ipc.KindCandidateGCStatus
		if action == "candidate-gc-enable" {
			kind = ipc.KindCandidateGCEnable
		}
		if action == "candidate-gc-disable" {
			kind = ipc.KindCandidateGCDisable
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
		return codeError("candidate_retention_scope_mismatch")
	}
	payload := coordinator.CandidateRetentionRequest[any]{ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken, Request: request}
	response, err := call(ctx, state, kind, payload)
	if err != nil {
		return err
	}
	_, err = out.Write(append(response.Payload, '\n'))
	return err
}
