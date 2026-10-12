package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"path/filepath"
	"strings"
)

type validationStartDiagnostic struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`
	Stage   string `json:"stage"`
	Code    string `json:"code"`
}

func validValidationStartDiagnostic(body []byte) bool {
	var d validationStartDiagnostic
	if len(body) > 256 || json.Unmarshal(body, &d) != nil || d.Version != 1 || d.Kind != "validation_start_failure" || d.Stage != "validation_start" || (d.Code != "validation_start_eagain" && d.Code != "validation_start_eintr") {
		return false
	}
	canonical, _ := json.Marshal(d)
	return bytes.Equal(canonical, body)
}

// Only this live call may finish its own starting phase with a consumed proof.
// The older refusal and every cold recovery path retain their previous fences.
func (h *Host) recordValidationStartRejected(ctx context.Context, g contract.LaunchCommand, cmd process.Command, cause error) error {
	bad := errors.New("validation_start_proof_invalid")
	p, err := adapter.ValidationRetryPayload(g.AdapterPayload)
	if err != nil || contract.RequireValidationTool(g, p.CandidateAction.CommandSHA256) != nil || ctx.Err() != nil || h.stopWasRequested(g.SegmentID) || h.producerID != g.HostID || cmd.PinnedPath != p.CandidateAction.Command[0] || cmd.PinnedSHA256 != p.CandidateAction.CommandSHA256 || cmd.SealedPath != h.validationSealedPath(g) || cmd.SealedSHA256 != p.CandidateAction.CommandSHA256 || (cmd.Path != cmd.SealedPath && (cmd.Path != "/usr/bin/sandbox-exec" || len(cmd.Args) < 3 || cmd.Args[2] != cmd.SealedPath)) || !filepath.IsAbs(cmd.SealedPath) {
		return bad
	}
	a := store.Attempt{ID: g.AttemptID, TaskID: g.TaskID, SegmentID: g.SegmentID}
	meta := metadataForLaunch(g)
	sp, err := h.spool(a)
	if err != nil {
		return err
	}
	existing, err := sp.Read()
	if err != nil {
		return err
	}
	if len(existing) != 1 {
		return bad
	}
	e := existing[0]
	if e.Kind != contract.EventPrepared || e.Process != nil || e.Artifact != nil || e.RunID != g.RunID || e.TaskID != g.TaskID || e.AttemptID != g.AttemptID || e.SegmentID != g.SegmentID || e.CommandID != g.CommandID || e.WorkRevision != g.WorkRevision || e.ProducerID != g.HostID {
		return bad
	}
	var phase map[string]any
	if sp.ReadMetadata("launch.json", &phase) != nil || phase["phase"] != "starting" || phase["process"] != nil || phase["run_id"] != g.RunID || phase["task_id"] != g.TaskID || phase["attempt_id"] != g.AttemptID || phase["segment_id"] != g.SegmentID || phase["executable"] != cmd.Path || phase["args_sha256"] != hashBytes([]byte(strings.Join(cmd.Args, "\x00"))) {
		return bad
	}
	code := process.ConsumeNeverStartedCode(ctx, cause, cmd)
	if code == "" {
		return bad
	}
	toolCleanup, err := h.removeValidationTool(g)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(validationStartDiagnostic{Version: 1, Kind: "validation_start_failure", Stage: "validation_start", Code: code})
	artifact, err := h.writeHostArtifact(meta, "terminal", 0, "prelaunch", "launch-failure", body)
	if err != nil {
		return err
	}
	if ctx.Err() != nil || h.stopWasRequested(g.SegmentID) {
		return bad
	}
	if err = publishLaunchPhase(sp, "not_started", a, g.RunID, g.TaskID, cmd, nil, errors.New(code)); err != nil {
		return err
	}
	_, err = h.appendStructuredEvent(sp, g.RunID, g.TaskID, a, meta, contract.EventLaunchRejected, artifact.SHA256, nil, nil, artifact, structuredEventFields{ValidationToolCleanup: toolCleanup, Failure: &contract.FailureFacts{Version: 1, Stage: "validation_start", Code: code, WorkerState: "not_started", ProbeState: "not_applicable"}})
	return err
}
