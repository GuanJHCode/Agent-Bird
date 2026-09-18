package main

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"slices"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Reconcile only an already-held capability. Never discover other capabilities,
// restart the coordinator/Host, or infer that an unobserved submission did not run.
func reconcileTaskHandle(ctx context.Context, path string, h taskHandle, cap controlCapability, out io.Writer) error {
	if h.Status != "submitting" && h.Status != "reconciled" {
		return codeError("task_reconciliation_not_required")
	}
	requestID, err := randomValue("reconcile")
	if err != nil {
		return err
	}
	body, err := json.Marshal(coordinator.TaskControlRequest{TaskID: h.TaskIDs[0], ControllerThread: cap.ControllerThread, ControlToken: cap.ControlToken})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, err := ipc.Call(ctx, filepath.Join(h.StateDir, "coordinator.sock"), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindSummary, RequestID: requestID, Payload: body})
	if err != nil {
		return codeError("task_reconciliation_unavailable")
	}
	if response.RequestID != requestID {
		return codeError("task_reconciliation_mismatch")
	}
	if response.Kind == ipc.KindError {
		var problem coordinator.ErrorResponse
		if decode(response.Payload, &problem) != nil || problem.Error == "" {
			return codeError("invalid_response")
		}
		return remoteError(problem.Error)
	}
	if response.Kind != ipc.KindResponse {
		return codeError("invalid_response")
	}
	var summary store.RunSummary
	if decode(response.Payload, &summary) != nil || summary.Version != 1 || summary.RunID != h.RunID {
		return codeError("task_reconciliation_mismatch")
	}
	expected := append([]string(nil), h.TaskIDs...)
	actual := make([]string, 0, len(summary.Tasks))
	for _, task := range summary.Tasks {
		actual = append(actual, task.TaskID)
	}
	slices.Sort(expected)
	slices.Sort(actual)
	if !slices.Equal(expected, actual) {
		return codeError("task_reconciliation_mismatch")
	}
	h.Status = "reconciled"
	if err = replacePrivateJSON(path, h); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(summary)
}
