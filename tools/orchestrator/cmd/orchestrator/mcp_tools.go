package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
)

type mcpBudget struct {
	PerSegmentMS int64  `json:"per_segment_ms"`
	TotalMS      int64  `json:"total_ms"`
	MaxAttempts  int    `json:"max_attempts"`
	GroupID      string `json:"group_id,omitempty"`
}
type mcpDispatchArgs struct {
	RequestID        string                  `json:"request_id"`
	Provider         string                  `json:"provider"`
	Role             adapter.Role            `json:"role"`
	Directory        string                  `json:"directory,omitempty"`
	Prompt           string                  `json:"prompt"`
	Paths            []string                `json:"paths,omitempty"`
	Acceptance       []string                `json:"acceptance"`
	Model            string                  `json:"model"`
	Reasoning        adapter.ReasoningEffort `json:"reasoning,omitempty"`
	GrokSessionWrite bool                    `json:"grok_session_write,omitempty"`
	Budget           mcpBudget               `json:"budget"`
}
type mcpReadArgs struct {
	Handle string `json:"handle"`
	TaskID string `json:"task_id,omitempty"`
}
type mcpWaitArgs struct {
	Handle    string `json:"handle"`
	TaskID    string `json:"task_id,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	TimeoutMS *int   `json:"timeout_ms,omitempty"`
}
type mcpCollectArgs struct {
	Handle             string `json:"handle"`
	TaskID             string `json:"task_id,omitempty"`
	Cursor             string `json:"cursor,omitempty"`
	IncludeDiagnostics bool   `json:"include_diagnostics,omitempty"`
}
type mcpAcceptArgs struct {
	Handle      string `json:"handle"`
	ReviewToken string `json:"review_token"`
	CommandID   string `json:"command_id"`
}
type mcpChangesArgs struct {
	Handle      string `json:"handle"`
	ReviewToken string `json:"review_token"`
	CommandID   string `json:"command_id"`
	Feedback    string `json:"feedback"`
	Acceptance  string `json:"acceptance"`
	Version     *int   `json:"version,omitempty"`
}
type mcpAckArgs struct {
	Handle                string              `json:"handle"`
	TaskID                string              `json:"task_id,omitempty"`
	DeliveryID            string              `json:"delivery_id"`
	CollectionProofSHA256 string              `json:"collection_proof_sha256"`
	Decisions             []store.AckDecision `json:"decisions"`
}
type mcpStopArgs struct {
	Handle       string `json:"handle"`
	TaskID       string `json:"task_id,omitempty"`
	WorkRevision int    `json:"work_revision"`
}

// Typed decoding, including exact argument names, preserves int64 event
// revisions. No generic map/float64 schema round trip can rewrite ACK tuples.
func mcpArguments(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := mcpArgumentKeys(raw, reflect.TypeOf(target).Elem()); err != nil {
		return err
	}
	if err := decode(raw, target); err != nil {
		return codeError("invalid_mcp_arguments")
	}
	return nil
}
func mcpArgumentKeys(raw json.RawMessage, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return codeError("invalid_mcp_arguments")
	}
	switch t.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			return codeError("invalid_mcp_arguments")
		}
		allowed := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			tag := strings.Split(field.Tag.Get("json"), ",")[0]
			if tag != "" && tag != "-" {
				allowed[tag] = field.Type
			}
		}
		for name, value := range fields {
			field, ok := allowed[name]
			if !ok {
				return codeError("invalid_mcp_arguments")
			}
			if err := mcpArgumentKeys(value, field); err != nil {
				return err
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return codeError("invalid_mcp_arguments")
		}
		for _, value := range values {
			if err := mcpArgumentKeys(value, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
func mcpWithHandle(value map[string]any, err error, handle string) (map[string]any, error) {
	if value == nil {
		value = map[string]any{}
	}
	value["handle"] = handle
	return value, err
}
func (f *mcpFacade) ownerForHandle(path string) (string, error) {
	if !f.routePathAllowed(path) {
		return "", codeError("mcp_route_scope_mismatch")
	}
	var h taskHandle
	if err := mcpReadPrivate(path, mcpConfigMaxBytes, &h); err != nil {
		return "", err
	}
	var reservation dispatchReservation
	if err := mcpReadPrivate(filepath.Join(filepath.Dir(path), "reservation.json"), mcpConfigMaxBytes, &reservation); err != nil {
		return "", err
	}
	if reservation.Version != 1 || reservation.RunID != h.RunID || reservation.ControllerThread == "" {
		return "", codeError("mcp_route_scope_mismatch")
	}
	return reservation.ControllerThread, nil
}
func (f *mcpFacade) dispatch(ctx context.Context, raw json.RawMessage) (map[string]any, error) {
	if err := f.checkConfiguration(); err != nil {
		return nil, err
	}
	if err := currentMCPSourceEnvironment(ctx); err != nil {
		if code := errorCode(err); code == "mcp_source_environment_changed" || code == "mcp_source_environment_unverified" {
			return map[string]any{"recovery": "reconfigure_mcp_and_restart_managed_controller"}, err
		}
		return nil, err
	}
	return f.dispatchMapped(ctx, raw)
}

// dispatchMapped maps arguments after admission. Only dispatch is registered as
// an MCP handler; mapping unit tests may exercise this layer independently.
func (f *mcpFacade) dispatchMapped(ctx context.Context, raw json.RawMessage) (map[string]any, error) {
	var input mcpDispatchArgs
	if err := mcpArguments(raw, &input); err != nil {
		return nil, err
	}
	if err := f.checkConfiguration(); err != nil {
		return nil, err
	}
	if input.Model == "" {
		return nil, codeError("mcp_model_required")
	}
	lock, ok := f.config.Providers[input.Provider]
	if !ok {
		return nil, codeError("mcp_provider_not_configured")
	}
	workspace, err := mcpCanonicalDirectory(f.config.Workspace)
	if err != nil || workspace != f.config.Workspace {
		return nil, codeError("mcp_workspace_changed")
	}
	if input.Directory != "" {
		directory, e := mcpCanonicalDirectory(input.Directory)
		if e != nil || directory != workspace {
			return nil, codeError("mcp_workspace_scope_mismatch")
		}
	}
	request := dispatchRequest{Version: 2, RequestID: input.RequestID, Provider: input.Provider, ProviderLock: lock, Directory: workspace, Prompt: input.Prompt, Role: input.Role, Paths: input.Paths, Acceptance: input.Acceptance, Model: input.Model, Reasoning: input.Reasoning, GrokSessionWrite: input.GrokSessionWrite, MaxAttempts: input.Budget.MaxAttempts, MaxActiveMSPerSegment: input.Budget.PerSegmentMS, MaxActiveMSTotal: input.Budget.TotalMS, BudgetGroupID: input.Budget.GroupID}
	if err = validateDispatch(request); err != nil {
		return nil, err
	}
	value, callErr := mcpCapture(func(out io.Writer) error {
		return withTaskRequest(request, func(path string) error {
			return f.invoke(ctx, []string{"dispatch", "--state-dir", f.config.StateDir, "--request", path}, out)
		})
	})
	if value == nil {
		return nil, callErr
	}
	path, _ := value["handle"].(string)
	delete(value, "handle")
	if path != "" {
		owner, e := f.ownerForHandle(path)
		if e != nil {
			return nil, e
		}
		route, e := f.mintRoute(path, owner)
		if e != nil {
			return nil, e
		}
		value["handle"] = route
		value["cli_handle"] = path
	}
	value["owner_scope"] = "managed_process"
	value["delivery_mode"] = "collect"
	return value, callErr
}
func (f *mcpFacade) inspect(ctx context.Context, raw json.RawMessage) (map[string]any, error) {
	var input mcpReadArgs
	if err := mcpArguments(raw, &input); err != nil {
		return nil, err
	}
	route, _, _, err := f.resolveRoute(input.Handle)
	if err != nil {
		return nil, err
	}
	task, err := mcpTask(route, input.TaskID)
	if err != nil {
		return nil, err
	}
	value, err := f.cli(ctx, []string{"inspect", "--handle", route.Handle, "--task-id", task})
	if err != nil {
		return mcpWithHandle(value, err, input.Handle)
	}
	data, e := json.Marshal(value)
	if e != nil {
		return nil, e
	}
	var summary store.RunSummary
	if e = json.Unmarshal(data, &summary); e != nil || summary.Version != 1 || summary.RunID != route.RunID {
		return nil, codeError("mcp_cli_result_invalid")
	}
	references := map[string]string{}
	for _, task := range summary.Tasks {
		if !slices.Contains(route.TaskIDs, task.TaskID) || task.ReviewableResult == nil || task.ReviewableResult.CandidateOID == "" {
			continue
		}
		reference, e := f.reviewReference(route, task.TaskID, *task.ReviewableResult)
		if e != nil {
			return nil, e
		}
		references[task.TaskID] = reference
	}
	value["review_tokens"] = references
	return mcpWithHandle(value, nil, input.Handle)
}
func (f *mcpFacade) wait(ctx context.Context, raw json.RawMessage) (map[string]any, error) {
	var input mcpWaitArgs
	if err := mcpArguments(raw, &input); err != nil {
		return nil, err
	}
	timeoutMS := 30000
	if input.TimeoutMS != nil {
		timeoutMS = *input.TimeoutMS
	}
	if timeoutMS < 1 || timeoutMS > 30000 || len(input.Cursor) > 4096 {
		return nil, codeError("invalid_mcp_arguments")
	}
	route, _, _, err := f.resolveRoute(input.Handle)
	if err != nil {
		return nil, err
	}
	task, err := mcpTask(route, input.TaskID)
	if err != nil {
		return nil, err
	}
	args := []string{"wait", "--handle", route.Handle, "--task-id", task, "--timeout-ms", strconv.Itoa(timeoutMS)}
	if input.Cursor != "" {
		args = append(args, "--cursor", input.Cursor)
	}
	value, err := f.cli(ctx, args)
	return mcpWithHandle(value, err, input.Handle)
}
func (f *mcpFacade) collect(ctx context.Context, raw json.RawMessage) (map[string]any, error) {
	var input mcpCollectArgs
	if err := mcpArguments(raw, &input); err != nil {
		return nil, err
	}
	if len(input.Cursor) > 4096 {
		return nil, codeError("invalid_mcp_arguments")
	}
	route, _, _, err := f.resolveRoute(input.Handle)
	if err != nil {
		return nil, err
	}
	task, err := mcpTask(route, input.TaskID)
	if err != nil {
		return nil, err
	}
	args := []string{"collect", "--handle", route.Handle, "--task-id", task}
	if input.Cursor != "" {
		args = append(args, "--cursor", input.Cursor)
	}
	if input.IncludeDiagnostics {
		args = append(args, "--include-diagnostics")
	}
	value, err := f.cli(ctx, args)
	return mcpWithHandle(value, err, input.Handle)
}
func (f *mcpFacade) accept(ctx context.Context, raw json.RawMessage) (map[string]any, error) {
	var input mcpAcceptArgs
	if err := mcpArguments(raw, &input); err != nil {
		return nil, err
	}
	if input.CommandID == "" || len(input.CommandID) > 128 {
		return nil, codeError("invalid_review")
	}
	route, _, _, err := f.resolveRoute(input.Handle)
	if err != nil {
		return nil, err
	}
	review, err := f.resolveReview(input.ReviewToken, route)
	if err != nil {
		return nil, err
	}
	binding := review.Binding
	request := map[string]any{"task_id": review.TaskID, "work_revision": binding.WorkRevision, "event_id": binding.EventID, "event_revision": binding.EventRevision, "event_hash": binding.EventHash, "action_slot": binding.ActionSlot, "decision": "accept", "command_id": input.CommandID}
	value, err := mcpCapture(func(out io.Writer) error {
		return withTaskRequest(request, func(path string) error {
			return f.invoke(ctx, []string{"accept", "--handle", route.Handle, "--request", path}, out)
		})
	})
	return mcpWithHandle(value, err, input.Handle)
}
func (f *mcpFacade) requestChanges(ctx context.Context, raw json.RawMessage) (map[string]any, error) {
	var input mcpChangesArgs
	if err := mcpArguments(raw, &input); err != nil {
		return nil, err
	}
	version := 3
	if input.Version != nil {
		version = *input.Version
	}
	if version != 2 && version != 3 {
		return nil, codeError("invalid_request_changes")
	}
	route, h, cap, err := f.resolveRoute(input.Handle)
	if err != nil {
		return nil, err
	}
	review, err := f.resolveReview(input.ReviewToken, route)
	if err != nil {
		return nil, err
	}
	b := review.Binding
	brief := ownerChangeBrief{Version: version, TaskID: review.TaskID, WorkRevision: b.WorkRevision, CandidateOID: b.CandidateOID, Feedback: input.Feedback, Acceptance: input.Acceptance, CommandID: input.CommandID}
	if !validOwnerChangeBrief(brief) {
		return nil, codeError("invalid_request_changes")
	}
	spec := store.OwnerReworkSpec{TaskID: review.TaskID, WorkRevision: b.WorkRevision, EventID: b.EventID, EventRevision: b.EventRevision, EventHash: b.EventHash, ActionSlot: b.ActionSlot, SegmentID: b.SegmentID, CandidateOID: b.CandidateOID, CommandID: input.CommandID, Feedback: input.Feedback, Acceptance: input.Acceptance}
	// Seed the original CLI checkpoint from the signed tuple, then use its
	// original receipt replay. No fresh summary may replace the source revision.
	value, err := mcpCapture(func(out io.Writer) error {
		if err := requireOwnerChangesCoordinator(ctx, route.StateDir); err != nil {
			return err
		}
		var body []byte
		if version == 3 {
			if err := requireRefsCoordinator(ctx, route.StateDir); err != nil {
				return err
			}
			body = ownerRefsBody(cap, brief)
			if _, err := adapter.DecodeReworkBody(body); err != nil {
				return codeError("request_changes_utf8_bytes_exceeded")
			}
		}
		if err := persistOwnerChangeSource(route.Handle, h, brief, spec, body); err != nil {
			return err
		}
		return withTaskRequest(brief, func(path string) error {
			return f.invoke(ctx, []string{"request-changes", "--handle", route.Handle, "--request", path}, out)
		})
	})
	return mcpWithHandle(value, err, input.Handle)
}
func (f *mcpFacade) ack(ctx context.Context, raw json.RawMessage) (map[string]any, error) {
	var input mcpAckArgs
	if err := mcpArguments(raw, &input); err != nil {
		return nil, err
	}
	if len(input.Decisions) == 0 || len(input.Decisions) > 64 || input.DeliveryID == "" || len(input.CollectionProofSHA256) != 64 {
		return nil, codeError("invalid_ack")
	}
	route, _, _, err := f.resolveRoute(input.Handle)
	if err != nil {
		return nil, err
	}
	task, err := mcpTask(route, input.TaskID)
	if err != nil {
		return nil, err
	}
	request := map[string]any{"version": 1, "task_id": task, "delivery_id": input.DeliveryID, "collection_proof_sha256": input.CollectionProofSHA256, "decisions": input.Decisions}
	value, err := mcpCapture(func(out io.Writer) error {
		return withTaskRequest(request, func(path string) error {
			return f.invoke(ctx, []string{"ack", "--handle", route.Handle, "--request", path}, out)
		})
	})
	return mcpWithHandle(value, err, input.Handle)
}
func (f *mcpFacade) stop(ctx context.Context, raw json.RawMessage) (map[string]any, error) {
	var input mcpStopArgs
	if err := mcpArguments(raw, &input); err != nil {
		return nil, err
	}
	if input.WorkRevision < 1 || input.WorkRevision > 2147483647 {
		return nil, codeError("invalid_args")
	}
	route, _, _, err := f.resolveRoute(input.Handle)
	if err != nil {
		return nil, err
	}
	task, err := mcpTask(route, input.TaskID)
	if err != nil {
		return nil, err
	}
	result, stopErr := f.cli(ctx, []string{"stop", "--handle", route.Handle, "--task-id", task, "--work-revision", strconv.Itoa(input.WorkRevision)})
	var requested any
	if stopErr == nil || errorCode(stopErr) == "host_unavailable" {
		requested = true
	} else {
		switch errorCode(stopErr) {
		case "owner_mismatch", "stale_work_revision", "conflict", "invalid_args", "task_not_found":
			requested = false
		}
	}
	observeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	observation, observeErr := f.cli(observeCtx, []string{"inspect", "--handle", route.Handle, "--task-id", task})
	confirmed := false
	if requested == true && observeErr == nil {
		raw, _ := json.Marshal(observation)
		var summary store.RunSummary
		if json.Unmarshal(raw, &summary) == nil && summary.RunID == route.RunID {
			for _, value := range summary.Tasks {
				if value.TaskID != task || value.WorkRevision != input.WorkRevision || value.ActiveSegments != 0 {
					continue
				}
				switch value.Status {
				case "completed", "cancelled", "failed", "budget_exhausted", "blocked_dependency", "result_ready", "interrupted", "waiting_question":
					confirmed = true
				}
			}
		}
	}
	value := map[string]any{"version": 1, "requested": requested, "confirmed": confirmed}
	if result != nil {
		value["request_result"] = result
	}
	if observeErr == nil {
		value["observation"] = observation
	} else {
		value["observation_error"] = errorCode(observeErr)
	}
	return mcpWithHandle(value, stopErr, input.Handle)
}
