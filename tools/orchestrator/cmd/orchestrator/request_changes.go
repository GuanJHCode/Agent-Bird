package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
)

type ownerChangeBrief struct {
	Version      int    `json:"version,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
	WorkRevision int    `json:"work_revision"`
	CandidateOID string `json:"candidate_oid"`
	Feedback     string `json:"feedback"`
	Acceptance   string `json:"acceptance"`
	CommandID    string `json:"command_id"`
}
type ownerChangeCheckpoint struct {
	Version     int                   `json:"version"`
	BodySHA256  string                `json:"body_sha256,omitempty"`
	RunID       string                `json:"run_id"`
	StateDir    string                `json:"state_dir"`
	ControlFile string                `json:"control_file"`
	Brief       ownerChangeBrief      `json:"brief"`
	Spec        store.OwnerReworkSpec `json:"spec"`
}

func ownerChangeCheckpointPath(handle string, h taskHandle, brief ownerChangeBrief) (string, error) {
	directory := filepath.Join(filepath.Dir(handle), "owner-decisions")
	if err := os.Mkdir(directory, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	path := filepath.Join(directory, dispatchHash(h.RunID+"\x00"+brief.TaskID+"\x00"+brief.CommandID)+".json")
	return path, privateHandleParent(path)
}

func ownerChangeSourceCheckpoint(h taskHandle, brief ownerChangeBrief, spec store.OwnerReworkSpec, body []byte) ownerChangeCheckpoint {
	saved := ownerChangeCheckpoint{Version: 1, RunID: h.RunID, StateDir: h.StateDir, ControlFile: h.ControlFile, Brief: brief, Spec: spec}
	if brief.Version == 3 {
		saved.Version = 3
		saved.Brief.Feedback, saved.Brief.Acceptance = "", ""
		saved.Spec.Feedback, saved.Spec.Acceptance = "", ""
		saved.BodySHA256 = dispatchHash(string(body))
	}
	return saved
}

// A signed MCP source can seed the same private checkpoint before the original
// CLI entry runs. Both entries then replay the identical durable source tuple.
// This file provides recovery metadata; every RPC still authenticates the owner.
func persistOwnerChangeSource(handle string, h taskHandle, brief ownerChangeBrief, spec store.OwnerReworkSpec, body []byte) error {
	path, err := ownerChangeCheckpointPath(handle, h, brief)
	if err != nil {
		return err
	}
	expected := ownerChangeSourceCheckpoint(h, brief, spec, body)
	if err = writeExclusiveJSON(path, expected); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	var actual ownerChangeCheckpoint
	if err = readPrivateJSON(path, &actual); err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, expected) {
		return codeError("request_changes_command_conflict")
	}
	return nil
}

func validOwnerChangeBrief(b ownerChangeBrief) bool {
	_, err := hex.DecodeString(b.CandidateOID)
	if b.WorkRevision < 1 || (len(b.CandidateOID) != 40 && len(b.CandidateOID) != 64) || err != nil || strings.ToLower(b.CandidateOID) != b.CandidateOID || b.CommandID == "" || len(b.CommandID) > 128 {
		return false
	}
	if b.Version != 0 && b.Version != 2 && b.Version != 3 {
		return false
	}
	limit := 16 * 1024
	if b.Version == 3 {
		limit = adapter.ReworkBodyMaxBytes
	}
	for _, s := range []string{b.Feedback, b.Acceptance} {
		if strings.TrimSpace(s) == "" || len(s) > limit || strings.ContainsRune(s, 0) || !utf8.ValidString(s) {
			return false
		}
	}
	return true
}

func taskRequestChanges(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("task request-changes", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	handle := fs.String("handle", "", "original private task handle")
	path := fs.String("request", "", "explicit owner feedback JSON")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *handle == "" || *path == "" {
		return codeError("invalid_args")
	}
	if err := privateHandleParent(*handle); err != nil {
		return err
	}
	var h taskHandle
	if err := readPrivateJSON(*handle, &h); err != nil {
		return err
	}
	if h.Version != 1 || h.RunID == "" || len(h.TaskIDs) == 0 || h.ControlFile == "" || !filepath.IsAbs(h.StateDir) {
		return codeError("task_handle_unresolved")
	}
	var brief ownerChangeBrief
	if err := readOwnerChangeInput(*path, &brief); err != nil {
		return err
	}
	if !validOwnerChangeBrief(brief) {
		return codeError("invalid_request_changes")
	}
	if brief.TaskID == "" {
		if len(h.TaskIDs) != 1 {
			return codeError("task_id_required")
		}
		brief.TaskID = h.TaskIDs[0]
	}
	if !slices.Contains(h.TaskIDs, brief.TaskID) {
		return codeError("task_handle_scope_mismatch")
	}
	cap, err := loadControlCapability(h.ControlFile)
	if err != nil {
		return err
	}
	bootstrap, err := loadHostBootstrap(cap)
	if err != nil {
		return err
	}
	if cap.RunID != h.RunID || bootstrap.SocketPath != filepath.Join(h.StateDir, "coordinator.sock") {
		return codeError("task_handle_scope_mismatch")
	}
	if err = requireOwnerChangesCoordinator(ctx, h.StateDir); err != nil {
		return err
	}
	var body []byte
	checkpointBrief := brief
	if brief.Version == 3 {
		if err = requireRefsCoordinator(ctx, h.StateDir); err != nil {
			return err
		}
		body = ownerRefsBody(cap, brief)
		if _, err = adapter.DecodeReworkBody(body); err != nil {
			return codeError("request_changes_utf8_bytes_exceeded")
		}
		checkpointBrief.Feedback = ""
		checkpointBrief.Acceptance = ""
	}
	checkpointPath, err := ownerChangeCheckpointPath(*handle, h, brief)
	if err != nil {
		return err
	}
	var saved ownerChangeCheckpoint
	if _, err = os.Lstat(checkpointPath); os.IsNotExist(err) {
		var summaryOut bytes.Buffer
		if err = taskFollowup(ctx, "inspect", []string{"--handle", *handle}, &summaryOut); err != nil {
			return err
		}
		var summary store.RunSummary
		if err = decode(summaryOut.Bytes(), &summary); err != nil || summary.RunID != h.RunID {
			return codeError("task_handle_scope_mismatch")
		}
		var selected *store.SummaryTask
		for i := range summary.Tasks {
			if summary.Tasks[i].TaskID == brief.TaskID {
				selected = &summary.Tasks[i]
			}
		}
		if selected == nil || selected.WorkRevision != brief.WorkRevision {
			return codeError("request_changes_stale")
		}
		if !slices.Contains(selected.AllowedActions, "request-changes") || selected.ReviewableResult == nil {
			if selected.RequestChangesBlockedReason != "" {
				return codeError(selected.RequestChangesBlockedReason)
			}
			return codeError("request_changes_unavailable")
		}
		binding := selected.ReviewableResult
		if binding.WorkRevision != brief.WorkRevision || binding.CandidateOID != brief.CandidateOID || binding.SegmentID == "" {
			return codeError("request_changes_stale")
		}
		spec := store.OwnerReworkSpec{TaskID: brief.TaskID, WorkRevision: brief.WorkRevision, EventID: binding.EventID, EventRevision: binding.EventRevision, EventHash: binding.EventHash, ActionSlot: binding.ActionSlot, SegmentID: binding.SegmentID, CandidateOID: brief.CandidateOID, Feedback: brief.Feedback, Acceptance: brief.Acceptance, CommandID: brief.CommandID}
		saved = ownerChangeSourceCheckpoint(h, brief, spec, body)
		if err = writeExclusiveJSON(checkpointPath, saved); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	} else if err != nil {
		return err
	}
	// Persist the exact source tuple before RPC. A retry never substitutes a newer
	// revision or result, even if the first request committed but its reply was lost.
	if err = readPrivateJSON(checkpointPath, &saved); err != nil {
		return err
	}
	if brief.Version == 3 {
		if saved.Version != 3 || saved.RunID != h.RunID || saved.StateDir != h.StateDir || saved.ControlFile != h.ControlFile || !reflect.DeepEqual(saved.Brief, checkpointBrief) || saved.BodySHA256 != dispatchHash(string(body)) || saved.Spec.TaskID != brief.TaskID || saved.Spec.WorkRevision != brief.WorkRevision || saved.Spec.CandidateOID != brief.CandidateOID || saved.Spec.CommandID != brief.CommandID || saved.Spec.Feedback != "" || saved.Spec.Acceptance != "" {
			return codeError("request_changes_command_conflict")
		}
		return callRequestChangesRefs(ctx, h.StateDir, cap, saved.Spec, body, out)
	}
	if saved.Version != 1 || saved.RunID != h.RunID || saved.StateDir != h.StateDir || saved.ControlFile != h.ControlFile || !reflect.DeepEqual(saved.Brief, brief) || saved.Spec.TaskID != brief.TaskID || saved.Spec.WorkRevision != brief.WorkRevision || saved.Spec.CandidateOID != brief.CandidateOID || saved.Spec.Feedback != brief.Feedback || saved.Spec.Acceptance != brief.Acceptance || saved.Spec.CommandID != brief.CommandID {
		return codeError("request_changes_command_conflict")
	}
	return callRequestChanges(ctx, h.StateDir, cap, saved.Spec, out)
}

func callRequestChanges(ctx context.Context, state string, cap controlCapability, spec store.OwnerReworkSpec, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, err := call(ctx, state, ipc.KindRequestChanges, coordinator.RequestChangesRequest{OwnerReworkSpec: spec, ControllerThread: cap.ControllerThread, ControlToken: cap.ControlToken})
	if err != nil {
		var rejected remoteError
		if errors.As(err, &rejected) {
			return err
		}
		// A timeout, broken frame or lost connection cannot prove the owner
		// decision did not commit. Preserve the durable source checkpoint and
		// retry only the same command/specification.
		return codeError("request_changes_uncertain")
	}
	_, err = out.Write(append(response.Payload, '\n'))
	return err
}

func requestChanges(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("request-changes", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "original private state")
	path := fs.String("request", "", "fully bound owner request")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *path == "" {
		return codeError("invalid_args")
	}
	var req struct {
		store.OwnerReworkSpec
		ControlFile string `json:"control_file"`
		Version     int    `json:"version,omitempty"`
	}
	if err := readOwnerChangeInput(*path, &req); err != nil {
		return err
	}
	if req.ControlFile == "" {
		return codeError("invalid_request_changes")
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	cap, err := loadControlCapability(req.ControlFile)
	if err != nil {
		return err
	}
	bootstrap, err := loadHostBootstrap(cap)
	if err != nil {
		return err
	}
	if bootstrap.SocketPath != filepath.Join(state, "coordinator.sock") {
		return codeError("task_handle_scope_mismatch")
	}
	if err = requireOwnerChangesCoordinator(ctx, state); err != nil {
		return err
	}
	if req.Version == 3 {
		if err = requireRefsCoordinator(ctx, state); err != nil {
			return err
		}
		b := ownerChangeBrief{Version: 3, TaskID: req.TaskID, WorkRevision: req.WorkRevision, Feedback: req.Feedback, Acceptance: req.Acceptance}
		body := ownerRefsBody(cap, b)
		spec := req.OwnerReworkSpec
		spec.Feedback = ""
		spec.Acceptance = ""
		return callRequestChangesRefs(ctx, state, cap, spec, body, out)
	}
	if req.Version != 0 && req.Version != 2 {
		return codeError("invalid_request_changes")
	}
	return callRequestChanges(ctx, state, cap, req.OwnerReworkSpec, out)
}
