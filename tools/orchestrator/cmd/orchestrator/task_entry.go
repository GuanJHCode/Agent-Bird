package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
)

// Task plans contain business scope, never owner/process or transport identity.
type taskPlan struct {
	RunID        string                    `json:"run_id"`
	PlanRevision int                       `json:"plan_revision"`
	Tasks        []coordinator.TaskRequest `json:"tasks"`
}

// A handle is routing metadata, not authority. The underlying CLI/service still
// validates the control capability and owner for every operation. It is never
// a source of cached task status, revisions, acceptance, or remaining budget.
type taskHandle struct {
	Version     int      `json:"version"`
	RunID       string   `json:"run_id"`
	StateDir    string   `json:"state_dir"`
	ControlFile string   `json:"control_file,omitempty"`
	TaskIDs     []string `json:"task_ids"`
	Status      string   `json:"status"`
}

func taskEntry(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return codeError("invalid_args")
	}
	switch args[0] {
	case "dispatch":
		return taskDispatch(ctx, args[1:], out)
	case "run", "submit":
		return taskSubmit(ctx, args[0], args[1:], out)
	case "probe":
		return taskProbe(ctx, args[1:], out)
	case "inspect", "status", "collect", "wait", "reconcile", "accept", "ack", "answer", "rework", "resume", "stop", "recover-host":
		return taskFollowup(ctx, args[0], args[1:], out)
	default:
		return codeError("invalid_args")
	}
}

func taskProvider(name string) (adapter.Provider, string, error) {
	switch name {
	case "codex", string(adapter.ProviderCodex):
		return adapter.ProviderCodex, "codex", nil
	case "claude", string(adapter.ProviderClaude):
		return adapter.ProviderClaude, "claude", nil
	case "agy", string(adapter.ProviderAGY):
		return adapter.ProviderAGY, "agy", nil
	case "grok", string(adapter.ProviderGrok):
		return adapter.ProviderGrok, "grok", nil
	default:
		return "", "", codeError("task_provider_unsupported")
	}
}

func taskProbe(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("task probe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	binary := fs.String("binary", "", "provider executable; defaults to PATH lookup")
	providerName := fs.String("provider", "claude", "codex, claude, agy, or grok")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return codeError("invalid_args")
	}
	provider, executable, err := taskProvider(*providerName)
	if err != nil {
		return err
	}
	if *binary == "" {
		*binary, err = exec.LookPath(executable)
		if err != nil {
			return codeError("provider_binary_not_found")
		}
	}
	canonical, err := filepath.EvalSymlinks(*binary)
	if err != nil {
		return codeError("provider_binary_not_found")
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return err
	}
	return withTaskRequest(providerRequest{Provider: provider, BinaryPath: canonical}, func(path string) error {
		return providerControl(ctx, "provider-probe", []string{"--request", path}, out)
	})
}

func taskSubmit(ctx context.Context, action string, args []string, out io.Writer) error {
	return taskSubmitPrepared(ctx, action, args, out, false)
}

// reusePreparing is internal to a locked, hash-verified dispatch reservation.
func taskSubmitPrepared(ctx context.Context, action string, args []string, out io.Writer, reusePreparing bool) error {
	fs := flag.NewFlagSet("task "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	handle := fs.String("handle", "", "new private handle file; never reused")
	stateArg := fs.String("state-dir", "", "private runtime state")
	planPath, runID, dir, promptPath, lockPath := "", "", "", "", ""
	var timeout int64
	providerName := "claude"
	model := ""
	grokSessionWrite := false
	if action == "submit" {
		fs.StringVar(&planPath, "request", "", "business plan JSON")
	} else {
		fs.StringVar(&model, "model", "", "model ID or cli-default")
		fs.StringVar(&providerName, "provider", "claude", "codex, claude, agy, or grok")
		fs.BoolVar(&grokSessionWrite, "grok-session-write", false, "explicitly authorize this Grok task's new session directory writes")
		fs.StringVar(&runID, "run-id", "", "unique run ID")
		fs.StringVar(&dir, "directory", "", "read-only workspace")
		fs.StringVar(&promptPath, "prompt-file", "", "private UTF-8 task brief")
		fs.StringVar(&lockPath, "provider-lock", "", "already confirmed provider lock")
		fs.Int64Var(&timeout, "timeout-ms", 0, "explicit total task budget")
	}
	if fs.Parse(args) != nil || fs.NArg() != 0 || *handle == "" {
		return codeError("invalid_args")
	}
	var plan taskPlan
	if action == "submit" {
		if planPath == "" {
			return codeError("invalid_args")
		}
		if err := readPrivateJSON(planPath, &plan); err != nil {
			return err
		}
	} else {
		if runID == "" || dir == "" || promptPath == "" || lockPath == "" || timeout < 1 || timeout > 3_600_000 {
			return codeError("invalid_args")
		}
		prompt, err := readTaskBrief(promptPath)
		if err != nil {
			return err
		}
		var lock adapter.ProviderLock
		if err = readPrivateJSON(lockPath, &lock); err != nil {
			return err
		}
		provider, _, err := taskProvider(providerName)
		if err != nil {
			return err
		}
		p := adapter.InvocationPayload{Provider: string(provider), Directory: dir, Prompt: string(prompt), ProviderLock: &lock, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: timeout}}
		p.Profile.GrokSessionWrite = grokSessionWrite
		if model == "cli-default" {
			p.Profile.CLIModelDefault = true
		} else {
			p.Profile.Model = adapter.ModelID(model)
		}
		if !adapter.ValidModelID(string(p.Profile.Model)) {
			return codeError("model_invalid")
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		plan = taskPlan{RunID: runID, PlanRevision: 1, Tasks: []coordinator.TaskRequest{{ID: runID + "-worker", MaxAttempts: 1, WorkRevision: 1, MaxActiveMS: timeout, AdapterPayload: raw, CompletionPolicy: "owner_review"}}}
	}
	if err := privateHandleParent(*handle); err != nil {
		return err
	}
	if _, err := os.Lstat(*handle); err == nil {
		if !reusePreparing || action != "submit" {
			return codeError("task_handle_exists")
		}
		var prior taskHandle
		if err = readPrivateJSON(*handle, &prior); err != nil {
			return err
		}
		expectedState, err := resolveState(*stateArg)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(plan.Tasks))
		for _, task := range plan.Tasks {
			ids = append(ids, task.ID)
		}
		if prior.Version != 1 || prior.RunID != plan.RunID || prior.StateDir != expectedState || !slices.Equal(prior.TaskIDs, ids) || prior.Status != "submitting" || prior.ControlFile != "" {
			return codeError("dispatch_checkpoint_invalid")
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if reusePreparing {
		return codeError("dispatch_checkpoint_invalid")
	}
	if plan.RunID == "" || plan.PlanRevision < 1 {
		return codeError("invalid_submit")
	}
	if err := prepareTaskWorkspaces(*handle, plan.Tasks); err != nil {
		return err
	}
	if err := preflightTasks(plan.Tasks); err != nil {
		return err
	}
	if err := preflightProviders(ctx, plan.Tasks); err != nil {
		return err
	}
	// Observe identity before reserving anything; never fall back to a shell PID.
	if _, err := currentOwner(ctx); err != nil {
		return err
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	if err = requireTaskCoordinator(ctx, state, plan.Tasks); err != nil {
		return err
	}
	if err = requireProviderCoordinator(ctx, state); err != nil {
		return err
	}
	h := taskHandle{Version: 1, RunID: plan.RunID, StateDir: state, Status: "submitting", TaskIDs: []string{}}
	for _, task := range plan.Tasks {
		if task.ID == "" || slices.Contains(h.TaskIDs, task.ID) || task.MaxAttempts < 1 || task.MaxActiveMS < 1 {
			return codeError("invalid_task_plan")
		}
		h.TaskIDs = append(h.TaskIDs, task.ID)
	}
	var ownerOut bytes.Buffer
	if err = ownerBind(ctx, []string{"--current", "--state-dir", state}, &ownerOut); err != nil {
		return err
	}
	var owner struct {
		Version          int    `json:"version"`
		OwnerMode        string `json:"owner_mode"`
		OwnerCapability  string `json:"owner_capability"`
		ControllerThread string `json:"controller_thread"`
		OriginContextID  string `json:"origin_context_id"`
		OriginPID        int    `json:"origin_pid"`
		OriginBirth      string `json:"origin_birth"`
		HostGeneration   string `json:"host_generation"`
	}
	if err = decode(ownerOut.Bytes(), &owner); err != nil {
		return err
	}
	if err = resolveTaskModels(ctx, state, owner.OwnerCapability, plan.Tasks); err != nil {
		return err
	}
	if err = preflightProviders(ctx, plan.Tasks); err != nil {
		return err
	}
	// An interrupted submission leaves this reservation in place. Never infer
	// from missing receipt/control that no launch happened, or silently resubmit.
	if reusePreparing {
		err = replacePrivateJSON(*handle, h)
	} else {
		err = writeExclusiveJSON(*handle, h)
	}
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return codeError("task_handle_exists")
		}
		return err
	}
	req := coordinator.SubmitRequest{RunID: plan.RunID, PlanRevision: plan.PlanRevision, Tasks: plan.Tasks, OwnerMode: "local", DeliveryMode: "collect", OwnerCapability: owner.OwnerCapability, ControllerThread: owner.ControllerThread, OriginContextID: owner.OriginContextID, OriginPID: owner.OriginPID, OriginBirth: owner.OriginBirth, HostGeneration: owner.HostGeneration}
	var receipt bytes.Buffer
	submitErr := withTaskRequest(req, func(path string) error {
		return submitWithPreparedControl(ctx, []string{"--state-dir", state, "--request", path}, &receipt, func(controlPath string) error {
			h.ControlFile = controlPath
			return replacePrivateJSON(*handle, h)
		})
	})
	var rejected remoteError
	if errors.As(submitErr, &rejected) {
		h.Status = "rejected"
		h.ControlFile = ""
		if err = replacePrivateJSON(*handle, h); err != nil {
			return err
		}
	}
	var r struct {
		Version     int    `json:"version"`
		Status      string `json:"status"`
		RunID       string `json:"run_id"`
		ControlFile string `json:"control_file"`
	}
	if receipt.Len() > 0 {
		if err = decode(receipt.Bytes(), &r); err != nil || r.RunID != h.RunID || r.ControlFile == "" {
			return codeError("task_receipt_invalid")
		}
		h.ControlFile, h.Status = r.ControlFile, r.Status
		if err = replacePrivateJSON(*handle, h); err != nil {
			// Preserve the original low-level receipt for reconciliation on disk error.
			_, _ = out.Write(receipt.Bytes())
			return err
		}
	}
	if err = json.NewEncoder(out).Encode(taskSubmissionReceipt(*handle, h, h.Status, false)); err != nil {
		return err
	}
	return submitErr
}

func readTaskBrief(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, codeError("path_not_absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !owned(info) || info.Size() > 256*1024 {
		return nil, codeError("task_brief_untrusted")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(body)) == "" || bytes.IndexByte(body, 0) >= 0 || !utf8.Valid(body) {
		return nil, codeError("task_brief_invalid")
	}
	return body, nil
}

func privateHandleParent(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return codeError("task_handle_path_untrusted")
	}
	parent := filepath.Dir(path)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return codeError("task_handle_path_untrusted")
	}
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !owned(info) {
		return codeError("task_handle_path_untrusted")
	}
	return nil
}

func withTaskRequest(value any, fn func(string) error) error {
	dir, err := os.MkdirTemp("", "codex-bird-task-request-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "request.json")
	if err = writeExclusiveJSON(path, value); err != nil {
		return err
	}
	return fn(path)
}

func preflightProviders(ctx context.Context, tasks []coordinator.TaskRequest) error {
	for _, task := range tasks {
		for _, raw := range append([]json.RawMessage{task.AdapterPayload}, task.Fallbacks...) {
			p, err := adapter.DecodeInvocationPayload(raw)
			if err != nil {
				return err
			}
			if p.Kind == "candidate" && p.CandidateAction != nil {
				continue
			}
			if p.Kind != "" || p.Profile == nil || p.ProviderLock == nil {
				return codeError("task_typed_profile_required")
			}
			if p.BinaryPath != "" || p.BinaryVersion != "" || p.BinarySHA256 != "" {
				return codeError("provider_lock_legacy_conflict")
			}
			req := adapter.Request{Provider: adapter.Provider(p.Provider), Binary: p.ProviderLock.Binary, Lock: p.ProviderLock, Profile: p.Profile, CWD: p.Directory, Prompt: p.Prompt, Session: adapter.SessionRef{Kind: adapter.SessionKind(p.SessionKind), ID: p.SessionID}, Permission: adapter.Permission{Mode: p.PermissionMode, Allow: p.Allow, Deny: p.Deny}, ExtraArgs: p.ExtraArgs, Workspace: p.CandidateWorkspace, Action: p.CandidateAction}
			if _, err = adapter.BuildInvocation(req); err != nil {
				return err
			}
			if err = adapter.VerifyExecutable(req.Binary, req.Binary.Version); err != nil {
				return err
			}
			actual, err := adapter.ProbeOutput(ctx, req.Binary.Path, "--version")
			if err != nil {
				return err
			}
			if err = adapter.VerifyExecutable(req.Binary, strings.TrimSpace(actual)); err != nil {
				return err
			}
			help, err := adapter.ProbeCapabilities(ctx, req.Provider, req.Binary.Path)
			if err != nil {
				return err
			}
			if err = adapter.CheckCapabilities(req, help); err != nil {
				return err
			}
		}
	}
	return nil
}

func taskFollowup(ctx context.Context, action string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("task "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("handle", "", "original task handle")
	request, taskID, cursor := "", "", ""
	includeDiagnostics := false
	timeoutMS := 30000
	revision := 0
	mutation := action == "accept" || action == "ack" || action == "answer" || action == "rework" || action == "recover-host"
	if mutation {
		fs.StringVar(&request, "request", "", "explicit owner decision JSON")
	} else {
		fs.StringVar(&taskID, "task-id", "", "task in this handle")
		if action == "collect" || action == "wait" {
			fs.StringVar(&cursor, "cursor", "", "opaque continuation cursor")
			fs.BoolVar(&includeDiagnostics, "include-diagnostics", false, "include incomplete progress checkpoints")
		}
		if action == "wait" {
			fs.IntVar(&timeoutMS, "timeout-ms", 30000, "bounded event wait, 1..30000 milliseconds")
		}
		if action == "resume" || action == "stop" {
			fs.IntVar(&revision, "work-revision", 0, "observed revision")
		}
	}
	if fs.Parse(args) != nil || fs.NArg() != 0 || *path == "" || (mutation && request == "") || timeoutMS < 1 || timeoutMS > 30000 || (action == "wait" && includeDiagnostics) {
		return codeError("invalid_args")
	}
	if err := privateHandleParent(*path); err != nil {
		return err
	}
	var h taskHandle
	if err := readPrivateJSON(*path, &h); err != nil {
		return err
	}
	if h.Version != 1 || h.RunID == "" || len(h.TaskIDs) == 0 || h.ControlFile == "" || !filepath.IsAbs(h.StateDir) {
		return codeError("task_handle_unresolved")
	}
	var decision map[string]json.RawMessage
	if mutation {
		if err := readPrivateJSON(request, &decision); err != nil {
			return err
		}
		if _, ok := decision["control_file"]; ok {
			return codeError("task_control_override_forbidden")
		}
		if json.Unmarshal(decision["task_id"], &taskID) != nil {
			return codeError("task_handle_scope_mismatch")
		}
		if raw, ok := decision["review_task_id"]; ok {
			var reviewID string
			if json.Unmarshal(raw, &reviewID) != nil || !slices.Contains(h.TaskIDs, reviewID) {
				return codeError("task_handle_scope_mismatch")
			}
		}
	} else if taskID == "" {
		if len(h.TaskIDs) > 1 && action != "inspect" && action != "reconcile" {
			return codeError("task_id_required")
		}
		taskID = h.TaskIDs[0]
	}
	if !slices.Contains(h.TaskIDs, taskID) {
		return codeError("task_handle_scope_mismatch")
	}
	cap, err := loadControlCapability(h.ControlFile)
	if err != nil {
		return err
	}
	if cap.RunID != h.RunID {
		return codeError("task_handle_scope_mismatch")
	}
	// Bind the route to its original runtime as well as its run; never guess a
	// default state directory when continuing from another conversation.
	b, err := loadHostBootstrap(cap)
	if err != nil {
		return err
	}
	if b.SocketPath != filepath.Join(h.StateDir, "coordinator.sock") {
		return codeError("task_handle_scope_mismatch")
	}
	if action == "reconcile" {
		return reconcileTaskHandle(ctx, *path, h, cap, out)
	}
	if mutation {
		decision["control_file"], _ = json.Marshal(h.ControlFile)
		return withTaskRequest(decision, func(p string) error {
			return run(ctx, []string{action, "--state-dir", h.StateDir, "--request", p}, out, io.Discard)
		})
	}
	if action == "inspect" {
		action = "summary"
	}
	if action == "wait" {
		action = "wait-events"
	}
	forwarded := []string{action, "--state-dir", h.StateDir, "--task-id", taskID, "--control-file", h.ControlFile}
	if action == "wait-events" {
		forwarded = append(forwarded, "--timeout-ms", strconv.Itoa(timeoutMS))
	}
	if includeDiagnostics {
		forwarded = append(forwarded, "--include-diagnostics")
	}
	if cursor != "" {
		forwarded = append(forwarded, "--cursor", cursor)
	}
	if revision != 0 {
		forwarded = append(forwarded, "--work-revision", strconv.Itoa(revision))
	}
	return run(ctx, forwarded, out, io.Discard)
}
