package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/instance"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The same versioned request is used by the CLI and all native plugin adapters.
// A call executes exactly one child; it does not retry or integrate a candidate.
type providerCallRequest struct {
	Version      int      `json:"version"`
	Provider     string   `json:"provider"`
	Task         string   `json:"task"`
	Directory    string   `json:"directory"`
	Mode         string   `json:"mode,omitempty"`
	Paths        []string `json:"paths,omitempty"`
	TimeoutMS    int64    `json:"timeout_ms,omitempty"`
	ProviderLock string   `json:"provider_lock,omitempty"`
}

type providerCallResult struct {
	Version       int              `json:"version"`
	CallID        string           `json:"call_id"`
	Provider      string           `json:"provider"`
	Status        string           `json:"status"`
	Result        string           `json:"result,omitempty"`
	FailureReason string           `json:"failure_reason,omitempty"`
	ChangedFiles  []string         `json:"changed_files"`
	ChangesKnown  bool             `json:"changes_known"`
	Workspace     string           `json:"workspace,omitempty"`
	Handle        string           `json:"handle,omitempty"`
	ProcessExited bool             `json:"process_exited"`
	TimingsMS     map[string]int64 `json:"timings_ms,omitempty"`
}

func normalizeProviderCall(r *providerCallRequest) error {
	if r.Version != 1 || !utf8.ValidString(r.Task) || len(r.Task) > 60000 || strings.TrimSpace(r.Task) == "" || strings.ContainsRune(r.Task, 0) || !filepath.IsAbs(r.Directory) || filepath.Clean(r.Directory) != r.Directory || r.Directory == "/" {
		return codeError("call_request_invalid")
	}
	switch r.Provider {
	case "codex", "claude", "agy", "grok":
	default:
		return codeError("call_provider_unsupported")
	}
	if r.Mode == "" {
		r.Mode = "read-only"
	}
	if r.TimeoutMS == 0 {
		r.TimeoutMS = 120000
	}
	if r.TimeoutMS < 1 || r.TimeoutMS > 3600000 {
		return codeError("call_timeout_invalid")
	}
	if r.Mode != "read-only" && r.Mode != "workspace-write" {
		return codeError("call_mode_invalid")
	}
	if r.Mode == "read-only" && len(r.Paths) > 0 || r.Mode == "workspace-write" && (len(r.Paths) == 0 || len(r.Paths) > 256) {
		return codeError("call_paths_required")
	}
	seen := map[string]bool{}
	for _, path := range r.Paths {
		if path == "" || path == "." || path == ".." || len(path) > 512 || filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path || strings.HasPrefix(path, "../") || strings.TrimSpace(path) != path || strings.ContainsAny(path, "*?[]{}(),\\") || seen[path] {
			return codeError("call_path_invalid")
		}
		for _, part := range strings.Split(path, "/") {
			if strings.EqualFold(part, ".git") || strings.EqualFold(part, ".codex") {
				return codeError("call_path_invalid")
			}
		}
		for _, r := range path {
			if unicode.IsControl(r) {
				return codeError("call_path_invalid")
			}
		}
		seen[path] = true
	}
	return nil
}

func providerCallEntry(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("call", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	request := fs.String("request", "", "private version 1 call request")
	stateArg := fs.String("state-dir", "", "private runtime state")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *request == "" {
		return codeError("invalid_args")
	}
	var req providerCallRequest
	if err := readPrivateJSON(*request, &req); err != nil {
		return err
	}
	result, err := executeProviderCall(ctx, req, *stateArg)
	if err != nil {
		result.Status = "failed"
		result.FailureReason = errorCode(err)
	}
	return json.NewEncoder(out).Encode(result)
}

func executeProviderCall(parent context.Context, req providerCallRequest, stateArg string) (result providerCallResult, retErr error) {
	metrics := newProviderCallMetrics()
	defer func() { metrics.finish(&result, retErr) }()
	parent = adapter.WithProbeCache(parent)
	result = providerCallResult{Version: 1, Provider: req.Provider, ChangedFiles: []string{}}
	if err := normalizeProviderCall(&req); err != nil {
		return result, err
	}
	// This authorization check also rejects invocation from a managed Worker.
	owner, err := currentOwner(parent)
	if err != nil {
		return result, err
	}
	state, err := resolveState(stateArg)
	if err != nil {
		return result, err
	}
	if err = instance.PrepareStateDir(state); err != nil {
		return result, err
	}
	state, err = filepath.EvalSymlinks(state)
	if err != nil {
		return result, err
	}
	lock, err := os.OpenFile(filepath.Join(state, "provider-call.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return result, err
	}
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !owned(info) {
		return result, codeError("call_lock_untrusted")
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return result, codeError("call_busy")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	journal := filepath.Join(state, "provider-call-active.json")
	if _, err = os.Lstat(journal); err == nil {
		return result, codeError("call_previous_unresolved")
	} else if !os.IsNotExist(err) {
		return result, err
	}
	if req.ProviderLock == "" {
		return result, codeError("provider_lock_required")
	}
	var pin adapter.ProviderLock
	if err = readPrivateJSON(req.ProviderLock, &pin); err != nil {
		return result, err
	}
	metrics.mark("prepare")
	if err = providerSettingsEntry(parent, []string{"enable", "--provider", req.Provider, "--provider-lock", req.ProviderLock, "--binary", pin.Binary.Path, "--state-dir", state}, io.Discard); err != nil {
		return result, err
	}
	id := make([]byte, 16)
	metrics.mark("preflight")
	if _, err = rand.Read(id); err != nil {
		return result, err
	}
	result.CallID = "call-" + hex.EncodeToString(id)
	// Persist the exact task-dispatch locator before it can submit. Even if the
	// caller dies before receiving stdout, the original durable handle is known.
	ownerKey := dispatchHash(owner.ControllerThread + "\x00" + owner.OriginBirth)
	requestKey := dispatchHash(result.CallID)
	result.Handle = filepath.Join(state, "dispatch", ownerKey[:24], requestKey[:24], "handle.json")
	if err = writeExclusiveJSON(journal, result); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(req.TimeoutMS)*time.Millisecond)
	defer cancel()
	role := adapter.Reviewer
	if req.Mode == "workspace-write" {
		role = adapter.Implementer
	}
	dispatch := dispatchRequest{Version: 1, RequestID: result.CallID, Provider: req.Provider, ProviderLock: req.ProviderLock, Directory: req.Directory, Prompt: req.Task, Role: role, Paths: req.Paths, Acceptance: []string{"Return the task result and describe any incomplete work."}, Model: "cli-default", MaxAttempts: 1, MaxActiveMS: req.TimeoutMS, GrokSessionWrite: req.Provider == "grok", GrokExistingAuthOnly: req.Provider == "grok"}
	if req.Provider == "codex" {
		dispatch.Model = string(adapter.CodexPluginCompatibilityModel)
	}
	var receipt bytes.Buffer
	dispatchErr := withTaskRequest(dispatch, func(path string) error {
		return taskDispatch(ctx, []string{"--request", path, "--state-dir", state}, &receipt)
	})
	metrics.mark("dispatch")
	var submitted struct {
		Handle string `json:"handle"`
	}
	_ = json.Unmarshal(receipt.Bytes(), &submitted)
	if submitted.Handle != "" && submitted.Handle != result.Handle {
		return result, codeError("call_handle_mismatch")
	}
	if submitted.Handle == "" {
		return result, dispatchErrOrUnknown(dispatchErr)
	}
	h, control, err := providerCallControl(result.Handle)
	if err != nil {
		return result, err
	}
	// On timeout/cancellation, stop through the original task route and wait for
	// its durable terminal state. A timeout is never represented as exit proof.
	boundReader := newBoundProviderCallReader(h.StateDir)
	reader := func(c context.Context, kind ipc.Kind, payload any) (json.RawMessage, error) {
		started := time.Now()
		response, err := boundReader(c, kind, payload)
		if kind == ipc.KindCallResult {
			metrics.add("result_rpc", started)
		} else {
			metrics.add("control_rpc", started)
		}
		return response, err
	}
	terminal, err := awaitProviderCall(ctx, control, reader)
	if err != nil && ctx.Err() != nil {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		raw, statusErr := reader(cleanup, ipc.KindStatus, control)
		var snapshot store.TaskSnapshot
		if statusErr == nil && json.Unmarshal(raw, &snapshot) == nil && snapshot.WorkRevision > 0 {
			control.WorkRevision = snapshot.WorkRevision
			if snapshot.SegmentStatus != "exited" && snapshot.SegmentStatus != "not_started" {
				_, _ = reader(cleanup, ipc.KindStopTask, control)
			}
			terminal, err = awaitProviderCall(cleanup, control, reader)
		}
		result.Status = "cancelled"
		result.FailureReason = "call_cancelled"
		if parent.Err() == nil {
			result.Status = "timed_out"
			result.FailureReason = "call_timeout"
		}
	}
	if err != nil {
		return result, err
	}
	metrics.mark("wait_and_collect")
	result.ProcessExited = terminal.ProcessExited
	if result.Status == "" {
		result.Status = terminal.Status
		if terminal.EventKind != contract.EventResult {
			result.FailureReason = terminal.EventKind
		}
	}
	result.ChangesKnown = req.Mode == "read-only"
	result.Result = terminal.Content
	if req.Mode == "workspace-write" && terminal.EventKind == contract.EventResult {
		var artifact struct {
			Version        int                     `json:"version"`
			ProviderResult string                  `json:"provider_result"`
			Candidate      gitops.CandidateReceipt `json:"candidate"`
		}
		if json.Unmarshal([]byte(terminal.Content), &artifact) != nil || artifact.Version != 1 || artifact.Candidate.Worktree == "" {
			return result, codeError("call_candidate_invalid")
		}
		result.ChangesKnown = true
		result.Result, result.Workspace = artifact.ProviderResult, artifact.Candidate.Worktree
		for _, change := range artifact.Candidate.Changes {
			result.ChangedFiles = append(result.ChangedFiles, change.Path)
		}
	}
	// Business acceptance remains an explicit caller decision through this handle.
	if err = os.Remove(journal); err != nil {
		return result, err
	}
	metrics.mark("finalize")
	return result, nil
}

func dispatchErrOrUnknown(err error) error {
	if err != nil {
		return err
	}
	return codeError("call_submission_unknown")
}

func providerCallControl(path string) (taskHandle, coordinator.TaskControlRequest, error) {
	var h taskHandle
	var control coordinator.TaskControlRequest
	if err := privateHandleParent(path); err != nil {
		return h, control, err
	}
	if err := readPrivateJSON(path, &h); err != nil {
		return h, control, err
	}
	if h.Version != 1 || len(h.TaskIDs) != 1 || !filepath.IsAbs(h.StateDir) {
		return h, control, codeError("task_handle_scope_mismatch")
	}
	cap, err := loadControlCapability(h.ControlFile)
	if err != nil {
		return h, control, err
	}
	bootstrap, err := loadHostBootstrap(cap)
	if err != nil {
		return h, control, err
	}
	if cap.RunID != h.RunID || bootstrap.SocketPath != filepath.Join(h.StateDir, "coordinator.sock") {
		return h, control, codeError("task_handle_scope_mismatch")
	}
	control = coordinator.TaskControlRequest{TaskID: h.TaskIDs[0], ControllerThread: cap.ControllerThread, ControlToken: cap.ControlToken}
	return h, control, nil
}

type providerCallReader func(context.Context, ipc.Kind, any) (json.RawMessage, error)

func awaitProviderCall(ctx context.Context, control coordinator.TaskControlRequest, read providerCallReader) (store.CallResult, error) {
	var result store.CallResult
	legacyWait := false
	for {
		kind, payload := ipc.KindWaitCallTerminal, any(coordinator.WaitCallTerminalRequest{TaskControlRequest: control, TimeoutMS: 30000})
		if legacyWait {
			kind, payload = ipc.KindStatus, control
		}
		raw, err := read(ctx, kind, payload)
		// A still-running older coordinator may share IPC v1. Retain its
		// authenticated status path rather than abandoning an active child.
		if !legacyWait && err != nil && errorCode(err) == "unsupported_request" {
			legacyWait = true
			continue
		}
		if err != nil {
			return result, err
		}
		var snapshot store.TaskSnapshot
		if err = json.Unmarshal(raw, &snapshot); err != nil {
			return result, err
		}
		// An owning Host may disconnect after committing its original Wait.
		// Durable exit evidence remains valid after that transport is gone.
		if snapshot.SegmentStatus == "exited" || snapshot.SegmentStatus == "not_started" {
			break
		}
		if snapshot.SegmentStatus == "unknown" || snapshot.HostStatus == "lost" || snapshot.HostStatus == "offline" {
			return result, codeError("call_process_unknown")
		}
		if legacyWait {
			select {
			case <-ctx.Done():
				return result, ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		} else if ctx.Err() != nil {
			return result, ctx.Err()
		}
	}
	var content strings.Builder
	offset := 0
	for {
		raw, err := read(ctx, ipc.KindCallResult, coordinator.CallResultRequest{TaskControlRequest: control, Offset: offset})
		if err != nil {
			return result, err
		}
		var page store.CallResult
		if err = json.Unmarshal(raw, &page); err != nil {
			return result, err
		}
		if page.Version != 1 || page.TotalBytes < 0 || page.TotalBytes > 1<<20 || page.NextOffset != offset+len(page.Content) || page.NextOffset > page.TotalBytes || !utf8.ValidString(page.Content) || (offset > 0 && (page.PayloadSHA256 != result.PayloadSHA256 || page.EventKind != result.EventKind || page.TotalBytes != result.TotalBytes || page.ProcessExited != result.ProcessExited || page.Status != result.Status)) {
			return result, codeError("call_result_page_invalid")
		}
		result = page
		content.WriteString(page.Content)
		if page.NextOffset == page.TotalBytes {
			result.Content = content.String()
			return result, nil
		}
		if page.NextOffset <= offset {
			return result, codeError("call_result_page_invalid")
		}
		offset = page.NextOffset
	}
}
