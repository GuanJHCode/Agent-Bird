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

func reportRetentionCLI(ctx context.Context, action string, args []string, out io.Writer) (err error) {
	defer func() {
		if errors.Is(err, context.Canceled) {
			err = codeError("report_retention_cancelled")
		} else if errors.Is(err, context.DeadlineExceeded) {
			err = codeError("report_retention_timeout")
		} else {
			err = diagnosticsCLIError(err)
		}
	}()
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "private state directory")
	controlFile := fs.String("control-file", "", "original run control capability")
	requestFile := fs.String("request", "", "owner-only report retention request or saved plan")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *controlFile == "" || *requestFile == "" {
		return codeError("invalid_args")
	}
	raw, err := readDiagnosticsRequestFile(*requestFile)
	if err != nil {
		return codeError("report_retention_request_file_invalid")
	}
	var request any
	var runID string
	var kind ipc.Kind
	switch action {
	case "report-artifact-retention-resolve":
		var r store.ReportArtifactResolutionRequest
		if json.Unmarshal(raw, &r) != nil {
			return codeError("report_retention_request_invalid")
		}
		request, runID, kind = r, r.RunID, ipc.KindReportArtifactResolve
	case "report-artifact-retention-plan":
		var r store.ReportArtifactRetentionPlanRequest
		if json.Unmarshal(raw, &r) != nil {
			return codeError("report_retention_request_invalid")
		}
		request, runID, kind = r, r.RunID, ipc.KindReportArtifactRetentionPlan
	case "report-artifact-retention-apply":
		var r store.ReportArtifactRetentionPlan
		if json.Unmarshal(raw, &r) != nil {
			return codeError("report_retention_plan_invalid")
		}
		request, runID, kind = r, r.RunID, ipc.KindReportArtifactRetentionApply
	case "report-gc-enable", "report-gc-disable", "report-gc-status":
		var r store.ReportGCRequest
		if json.Unmarshal(raw, &r) != nil {
			return codeError("report_retention_request_invalid")
		}
		request, runID, kind = r, r.RunID, ipc.KindReportGCStatus
		if action == "report-gc-enable" {
			kind = ipc.KindReportGCEnable
		}
		if action == "report-gc-disable" {
			kind = ipc.KindReportGCDisable
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
		return codeError("report_retention_scope_mismatch")
	}
	payload := coordinator.ReportRetentionRequest[any]{ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken, Request: request}
	response, err := call(ctx, state, kind, payload)
	if err != nil {
		return err
	}
	_, err = out.Write(append(response.Payload, '\n'))
	return err
}
