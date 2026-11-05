package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
	"codex-cli-orchestration-design/tools/orchestrator/internal/instance"
)

type dispatchRequest struct {
	BudgetGroupID         string                  `json:"budget_group_id,omitempty"`
	MaxActiveMSPerSegment int64                   `json:"max_active_ms_per_segment,omitempty"`
	MaxActiveMSTotal      int64                   `json:"max_active_ms_total,omitempty"`
	Version               int                     `json:"version"`
	RequestID             string                  `json:"request_id"`
	Provider              string                  `json:"provider"`
	ProviderLock          string                  `json:"provider_lock"`
	Directory             string                  `json:"directory"`
	Prompt                string                  `json:"prompt"`
	Role                  adapter.Role            `json:"role"`
	Paths                 []string                `json:"paths,omitempty"`
	Acceptance            []string                `json:"acceptance"`
	Model                 string                  `json:"model,omitempty"`
	MaxAttempts           int                     `json:"max_attempts"`
	MaxActiveMS           int64                   `json:"max_active_ms"`
	GrokSessionWrite      bool                    `json:"grok_session_write,omitempty"`
	Reasoning             adapter.ReasoningEffort `json:"reasoning,omitempty"`
}

type dispatchReservation struct {
	Version          int    `json:"version"`
	RequestSHA256    string `json:"request_sha256"`
	ControllerThread string `json:"controller_thread"`
	RunID            string `json:"run_id"`
	Phase            string `json:"phase"`
	PlanSHA256       string `json:"plan_sha256,omitempty"`
}

var dispatchID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)
var dispatchErrorCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)

func dispatchRejectionCode(reason string) string {
	if !dispatchErrorCode.MatchString(reason) {
		return "submit_rejected_error_unavailable"
	}
	return reason
}

var dispatchOID = regexp.MustCompile(`^[a-f0-9]{40}$`)

func validateDispatch(req dispatchRequest) error {
	if req.Reasoning != "" && req.Reasoning != "low" && req.Reasoning != "medium" && req.Reasoning != "high" {
		return codeError("reasoning_unsupported")
	}
	if (req.Version != 1 && req.Version != 2) || !dispatchID.MatchString(req.RequestID) || req.ProviderLock == "" || req.Directory == "" || strings.TrimSpace(req.Prompt) == "" || !utf8.ValidString(req.Prompt) || strings.ContainsRune(req.Prompt, 0) || len(req.Prompt) > 256*1024 || req.MaxAttempts < 1 || req.MaxAttempts > contract.MaxAttempts || len(req.Acceptance) < 1 || len(req.Acceptance) > 64 {
		return codeError("dispatch_request_invalid")
	}
	if req.Version == 1 {
		if req.MaxActiveMS < 1 || req.MaxActiveMS > 3600000 || req.MaxActiveMSPerSegment != 0 || req.MaxActiveMSTotal != 0 || req.BudgetGroupID != "" {
			return codeError("dispatch_request_invalid")
		}
	} else if req.MaxActiveMS != 0 || req.MaxActiveMSPerSegment < 1 || req.MaxActiveMSPerSegment > 3600000 || req.MaxActiveMSTotal < 1 || req.MaxActiveMSTotal > 7200000 || (req.BudgetGroupID != "" && !dispatchID.MatchString(req.BudgetGroupID)) {
		return codeError("budget_policy_invalid")
	}
	p, _, err := taskProvider(req.Provider)
	if err != nil {
		return err
	}
	if req.Role != adapter.Reviewer && req.Role != adapter.Implementer {
		return codeError("dispatch_request_invalid")
	}
	if req.Role == adapter.Implementer && (len(req.Paths) == 0 || len(req.Paths) > 256) {
		return codeError("dispatch_paths_required")
	}
	if req.Role == adapter.Reviewer && len(req.Paths) > 0 {
		return codeError("dispatch_readonly_paths_unsupported")
	}
	seen := map[string]bool{}
	for _, path := range req.Paths {
		if path == "" || path == "." || path == ".." || len(path) > 512 || filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path || strings.HasPrefix(path, "../") || strings.TrimSpace(path) != path || strings.ContainsAny(path, "*?[]{}(),\\") || seen[path] {
			return codeError("dispatch_path_invalid")
		}
		for _, part := range strings.Split(path, "/") {
			if strings.EqualFold(part, ".git") {
				return codeError("dispatch_path_invalid")
			}
		}
		for _, r := range path {
			if unicode.IsControl(r) {
				return codeError("dispatch_path_invalid")
			}
		}
		seen[path] = true
	}
	for _, condition := range req.Acceptance {
		if strings.TrimSpace(condition) == "" || len(condition) > 8192 || !utf8.ValidString(condition) || strings.ContainsRune(condition, 0) {
			return codeError("dispatch_acceptance_invalid")
		}
	}
	if !adapter.ValidModelID(req.Model) {
		return codeError("model_invalid")
	}
	if p == adapter.ProviderGrok && !req.GrokSessionWrite {
		return codeError("grok_session_write_required")
	}
	if p != adapter.ProviderGrok && req.GrokSessionWrite {
		return codeError("profile_provider_state_mismatch")
	}
	return nil
}

// Read-only Git observations, with optional locks and fsmonitor execution disabled.
// No checkout, add, commit, hook installation, or automatic dirty-input snapshot.
func dispatchSourceBase(ctx context.Context, directory string) (string, error) {
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "/usr/bin/git", append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-C", directory}, args...)...)
		cmd.Env = os.Environ()
		for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES"} {
			cmd.Env = environmentWithout(cmd.Env, key)
		}
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	top, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return "", codeError("dispatch_source_not_repository")
	}
	top, err = filepath.EvalSymlinks(top)
	if err != nil || top != directory {
		return "", codeError("dispatch_repository_root_required")
	}
	base, err := git("rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || !dispatchOID.MatchString(base) {
		return "", codeError("dispatch_base_unavailable")
	}
	status, err := git("status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return "", codeError("dispatch_source_unavailable")
	}
	if status != "" {
		return "", codeError("dispatch_source_dirty")
	}
	now, err := git("rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || now != base {
		return "", codeError("dispatch_source_changed")
	}
	return base, nil
}

func dispatchHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func dispatchPlan(req dispatchRequest, lock adapter.ProviderLock, runID, base string) (taskPlan, error) {
	provider, _, err := taskProvider(req.Provider)
	if err != nil {
		return taskPlan{}, err
	}
	active := req.MaxActiveMS
	var budget *contract.BudgetPolicy
	group := runID + "-budget"
	if req.Version == 2 {
		active = req.MaxActiveMSPerSegment
		budget = &contract.BudgetPolicy{Version: 2, MaxActiveMSPerSegment: active, MaxActiveMSTotal: req.MaxActiveMSTotal}
		if req.BudgetGroupID != "" {
			group = req.BudgetGroupID
		}
	}
	profile := &adapter.ExecutionProfile{Version: 1, Role: req.Role, Permission: adapter.ReadOnly, TimeoutMS: active, GrokSessionWrite: req.GrokSessionWrite, Reasoning: req.Reasoning}
	if req.Model == "cli-default" {
		profile.CLIModelDefault = true
	} else {
		profile.Model = adapter.ModelID(req.Model)
	}
	payload := adapter.InvocationPayload{Provider: string(provider), ProviderLock: &lock, Profile: profile, Directory: req.Directory, Prompt: req.Prompt + "\n\nAcceptance conditions:\n- " + strings.Join(req.Acceptance, "\n- ")}
	if req.Role == adapter.Implementer {
		profile.Permission = adapter.WorkspaceWrite
		payload.Directory = ""
		payload.CandidateWorkspace = &adapter.CandidateWorkspace{Version: 1, RepoRoot: req.Directory, BaseOID: base, Paths: append([]string(nil), req.Paths...), AutoDirectory: true}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return taskPlan{}, err
	}
	return taskPlan{RunID: runID, PlanRevision: 1, Tasks: []coordinator.TaskRequest{{ID: runID + "-work", MaxAttempts: req.MaxAttempts, MaxActiveMS: active, BudgetPolicy: budget, BudgetGroupID: group, CompletionPolicy: "owner_review", AdapterPayload: raw}}}, nil
}

func taskDispatch(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("task dispatch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("request", "", "private business request JSON")
	stateArg := fs.String("state-dir", "", "private runtime state")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *path == "" {
		return codeError("invalid_args")
	}
	var req dispatchRequest
	if err := readPrivateJSON(*path, &req); err != nil {
		return err
	}
	if err := validateDispatch(req); err != nil {
		return err
	}
	absolute, err := filepath.Abs(req.Directory)
	if err != nil {
		return err
	}
	req.Directory, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return codeError("profile_workspace_untrusted")
	}
	if info, e := os.Stat(req.Directory); e != nil || !info.IsDir() {
		return codeError("profile_workspace_untrusted")
	}
	req.ProviderLock, err = filepath.Abs(req.ProviderLock)
	if err != nil {
		return err
	}
	owner, err := currentOwner(ctx)
	if err != nil {
		return err
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	if err = instance.PrepareStateDir(state); err != nil {
		return err
	}
	state, err = filepath.EvalSymlinks(state)
	if err != nil {
		return err
	}
	// Validate peer ancestry before serving even an existing dispatch handle.
	// A worker inherits the launcher context but may never recurse through the
	// idempotent fast path or reserve new dispatch state.
	if err = ownerBind(ctx, []string{"--current", "--state-dir", state}, io.Discard); err != nil {
		return err
	}
	// Controller identity selects a namespace, not a capability.
	ownerKey := dispatchHash(owner.ControllerThread + "\x00" + owner.OriginBirth)
	requestKey := dispatchHash(req.RequestID)
	directory := state
	for _, part := range []string{"dispatch", ownerKey[:24], requestKey[:24]} {
		directory = filepath.Join(directory, part)
		if err = os.Mkdir(directory, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		if err = privateHandleParent(filepath.Join(directory, "handle.json")); err != nil {
			return err
		}
	}
	lockFile, err := os.OpenFile(filepath.Join(directory, "request.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return err
	}
	defer lockFile.Close()
	info, err := lockFile.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !owned(info) {
		return codeError("dispatch_lock_untrusted")
	}
	if err = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return codeError("dispatch_busy")
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	runID := "bird-" + dispatchHash(ownerKey + "\x00" + req.RequestID)[:24]
	expected := dispatchReservation{Version: 1, RequestSHA256: dispatchHash(string(raw)), ControllerThread: owner.ControllerThread, RunID: runID}
	record := filepath.Join(directory, "reservation.json")
	handle := filepath.Join(directory, "handle.json")
	checkpoint, h, err := loadDispatchCheckpoint(directory, state, expected)
	if err != nil {
		return err
	}
	if h != nil && (h.ControlFile != "" || h.Status != "submitting") {
		if h.Status == "rejected" {
			reason := dispatchRejectionCode(h.Error)
			_ = json.NewEncoder(out).Encode(map[string]any{"version": 1, "status": "rejected", "error": reason, "handle": handle, "run_id": runID, "task_ids": h.TaskIDs, "existing": true})
			return codeError(reason)
		}
		if h.Status == "submitting" || h.Status == "submit_uncertain" {
			if err = taskFollowup(ctx, "reconcile", []string{"--handle", handle}, io.Discard); err != nil {
				_ = json.NewEncoder(out).Encode(map[string]any{"version": 1, "status": h.Status, "handle": handle, "run_id": runID, "existing": true})
				return err
			}
			if err = readPrivateJSON(handle, h); err != nil {
				return err
			}
		}
		return json.NewEncoder(out).Encode(map[string]any{"version": 1, "status": h.Status, "handle": handle, "run_id": runID, "task_ids": h.TaskIDs, "existing": true})
	}
	planPath := filepath.Join(directory, "plan.json")
	if checkpoint.Phase == "preparing" {
		if _, err = os.Lstat(record); os.IsNotExist(err) {
			if err = writeExclusiveJSON(record, checkpoint); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		var providerLock adapter.ProviderLock
		if err = readPrivateJSON(req.ProviderLock, &providerLock); err != nil {
			return err
		}
		provider, _, _ := taskProvider(req.Provider)
		if providerLock.Provider != provider {
			return codeError("provider_lock_invalid")
		}
		base := ""
		if req.Role == adapter.Implementer {
			base, err = dispatchSourceBase(ctx, req.Directory)
			if err != nil {
				return err
			}
		}
		plan, err := dispatchPlan(req, providerLock, runID, base)
		if err != nil {
			return err
		}
		if err = prepareTaskWorkspaces(state, handle, plan.Tasks); err != nil {
			return err
		}
		if err = preflightTasks(plan.Tasks); err != nil {
			return err
		}
		if err = preflightProviders(ctx, plan.Tasks); err != nil {
			return err
		}
		if _, err = os.Lstat(planPath); os.IsNotExist(err) {
			err = writeExclusiveJSON(planPath, plan)
		} else if err == nil {
			err = replacePrivateJSON(planPath, plan)
		}
		if err != nil {
			return err
		}
		planBytes, err := os.ReadFile(planPath)
		if err != nil {
			return err
		}
		checkpoint.Phase = "prepared"
		checkpoint.PlanSHA256 = dispatchHash(string(planBytes))
		if err = replacePrivateJSON(record, checkpoint); err != nil {
			return err
		}
	}
	// No handle, or a handle without a prepared control path, proves this v1
	// dispatch never crossed the durable pre-RPC barrier. Only this locked
	// reservation can continue preparation; submitted/unknown work cannot.
	return taskSubmitPrepared(ctx, "submit", []string{"--request", planPath, "--handle", handle, "--state-dir", state}, out, h != nil)
}

func loadDispatchCheckpoint(directory, state string, expected dispatchReservation) (dispatchReservation, *taskHandle, error) {
	record := filepath.Join(directory, "reservation.json")
	if _, err := os.Lstat(record); os.IsNotExist(err) {
		expected.Phase = "preparing"
		return expected, nil, nil
	} else if err != nil {
		return dispatchReservation{}, nil, err
	}
	var current dispatchReservation
	if err := readPrivateJSON(record, &current); err != nil {
		return current, nil, err
	}
	if current.Version != expected.Version || current.RequestSHA256 != expected.RequestSHA256 || current.ControllerThread != expected.ControllerThread || current.RunID != expected.RunID {
		return current, nil, codeError("dispatch_request_conflict")
	}
	if current.Phase != "preparing" && current.Phase != "prepared" {
		return current, nil, codeError("dispatch_checkpoint_invalid")
	}
	if current.Phase == "prepared" {
		planPath := filepath.Join(directory, "plan.json")
		var plan taskPlan
		if err := readPrivateJSON(planPath, &plan); err != nil {
			return current, nil, err
		}
		raw, err := os.ReadFile(planPath)
		if err != nil || current.PlanSHA256 != dispatchHash(string(raw)) || plan.RunID != current.RunID || plan.PlanRevision != 1 {
			return current, nil, codeError("dispatch_plan_changed")
		}
	}
	path := filepath.Join(directory, "handle.json")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return current, nil, nil
	} else if err != nil {
		return current, nil, err
	}
	if current.Phase != "prepared" {
		return current, nil, codeError("dispatch_checkpoint_invalid")
	}
	var h taskHandle
	if err := readPrivateJSON(path, &h); err != nil {
		return current, nil, err
	}
	if h.Version != 1 || h.RunID != current.RunID || h.StateDir != state || len(h.TaskIDs) != 1 || h.TaskIDs[0] != current.RunID+"-work" {
		return current, nil, codeError("task_handle_scope_mismatch")
	}
	switch h.Status {
	case "submitting", "rejected", "submit_uncertain", "queued", "queued_host_unavailable", "reconciled":
	default:
		return current, nil, codeError("dispatch_checkpoint_invalid")
	}
	if h.Status != "submitting" && h.Status != "rejected" && h.ControlFile == "" {
		return current, nil, codeError("task_handle_unresolved")
	}
	return current, &h, nil
}
