package codexsession

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strconv"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexconfig"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
)

const (
	maxEarlyEvents = 32
	maxEarlyBytes  = 256 << 10
	maxItems       = 256
	maxFinalBytes  = 64 << 10
)

// All mutable state belongs to the Run goroutine. Views retain only identities,
// hashes and candidate final text, never native error, reasoning or tool prose.
type sessionState struct {
	ctx                                 context.Context
	client                              *codexrpc.Client
	shapes                              *nativeShapes
	projection                          *codexconfig.Projection
	options                             Options
	phase                               string
	out                                 Outcome
	model, effort, tier                 any
	callbackDone, permission            bool
	provisionalThread, provisionalTurn  string
	threadStarted                       *[32]byte
	threadPersistence                   *[2]string
	startedTurn                         *[32]byte
	earlyEvents, earlyBytes, finalBytes int
	items                               map[string]*itemView
	requests                            map[string]bool
	terminal                            *turnView
	failure                             FailureFacts
}
type itemView struct {
	id, kind, phase, text string
	hash                  [32]byte
	completed             bool
}
type turnView struct {
	id, status, view string
	hash             [32]byte
	items            []itemView
	hasError         bool
}

func newSessionState(ctx context.Context, client *codexrpc.Client, shapes *nativeShapes, projection *codexconfig.Projection, options Options) *sessionState {
	return &sessionState{ctx: ctx, client: client, shapes: shapes, projection: projection, options: options, items: make(map[string]*itemView), requests: make(map[string]bool)}
}
func (s *sessionState) initialize(value map[string]any) bool {
	return value["codexHome"] == s.options.OwnedHome && value["platformFamily"] == s.options.PlatformFamily && value["platformOs"] == s.options.PlatformOS && validScalar(str(value["userAgent"]))
}
func (s *sessionState) config(response map[string]any) error {
	config := obj(response["config"])
	raw, err := json.Marshal(config)
	if err != nil || s.projection.VerifyEffective(raw) != nil || config["sandbox_mode"] != "read-only" {
		return ErrPolicy
	}
	s.model = config["model"]
	s.effort = config["model_reasoning_effort"]
	s.tier = config["service_tier"]
	if s.model != nil && !validScalar(str(s.model)) {
		return ErrPolicy
	}
	return nil
}
func (s *sessionState) threadParams() (string, map[string]any) {
	params := map[string]any{"cwd": s.options.CWD, "modelProvider": "openai", "approvalPolicy": "on-request", "approvalsReviewer": "user", "sandbox": "read-only"}
	if s.model != nil {
		params["model"] = s.model
	}
	if s.tier != nil {
		params["serviceTier"] = s.tier
	}
	if s.options.ResumeThreadID != "" {
		params["threadId"] = s.options.ResumeThreadID
		params["excludeTurns"] = true
		return "thread/resume", params
	}
	params["ephemeral"] = false
	return "thread/start", params
}
func (s *sessionState) turnParams(binding ThreadBinding, effort, tier any) map[string]any {
	var schema any
	_ = json.Unmarshal([]byte(adapter.ManagedTaskSchema()), &schema)
	params := map[string]any{"threadId": binding.ThreadID, "cwd": s.options.CWD, "model": binding.Model, "approvalPolicy": "on-request", "approvalsReviewer": "user", "sandboxPolicy": map[string]any{"type": "readOnly", "networkAccess": false}, "input": []any{map[string]any{"type": "text", "text": s.options.Prompt + "\n\n" + adapter.ManagedTaskInstruction(), "text_elements": []any{}}}, "outputSchema": schema}
	if effort != nil {
		params["effort"] = effort
	}
	if tier != nil {
		params["serviceTier"] = tier
	}
	return params
}
func (s *sessionState) bindThread(response map[string]any) error {
	model := str(response["model"])
	if !validScalar(model) || response["modelProvider"] != "openai" || !s.policy(response, "sandbox") || s.model != nil && s.model != model || s.effort != nil && s.effort != response["reasoningEffort"] || s.tier != nil && s.tier != response["serviceTier"] {
		return ErrPolicy
	}
	if roots, present := response["runtimeWorkspaceRoots"]; present && !s.workspaceRoots(roots) {
		return ErrPolicy
	}
	if mode, present := response["multiAgentMode"]; present && mode != "explicitRequestOnly" {
		return ErrPolicy
	}
	if response["activePermissionProfile"] != nil || response["initialTurnsPage"] != nil {
		return ErrPolicy
	}
	thread := obj(response["thread"])
	if value := thread["reasoningEffort"]; value != nil && value != response["reasoningEffort"] {
		return ErrPolicy
	}
	if err := s.thread(thread, model); err != nil {
		return err
	}
	id := str(thread["id"])
	if s.provisionalThread != "" && s.provisionalThread != id || s.options.ResumeThreadID != "" && s.options.ResumeThreadID != id {
		return ErrProtocol
	}
	s.out.Binding = ThreadBinding{ThreadID: id, Model: model, ModelProvider: "openai", RolloutPath: str(thread["path"]), HistoryMode: str(thread["historyMode"])}
	s.model = model
	s.effort = response["reasoningEffort"]
	s.tier = response["serviceTier"]
	if s.options.ResumeThreadID == "" {
		s.earlyEvents = 0
		s.earlyBytes = 0
	}
	return nil
}
func (s *sessionState) policy(value map[string]any, sandbox string) bool {
	return value["cwd"] == s.options.CWD && value["approvalPolicy"] == "on-request" && value["approvalsReviewer"] == "user" && readOnly(obj(value[sandbox]))
}
func readOnly(value map[string]any) bool {
	return value["type"] == "readOnly" && (value["networkAccess"] == nil || value["networkAccess"] == false)
}
func (s *sessionState) workspaceRoots(value any) bool {
	roots := arr(value)
	return len(roots) == 1 && roots[0] == s.options.CWD
}
func (s *sessionState) thread(thread map[string]any, model string) error {
	if !validScalar(str(thread["id"])) || !validScalar(str(thread["sessionId"])) || thread["cwd"] != s.options.CWD || thread["modelProvider"] != "openai" || thread["ephemeral"] != false || obj(thread["status"])["type"] != "idle" || len(arr(thread["turns"])) != 0 {
		return ErrPolicy
	}
	if value, present := thread["environments"]; present {
		environments := arr(value)
		if len(environments) != 1 {
			return ErrPolicy
		}
		environment := obj(environments[0])
		if environment["environmentId"] != "local" || environment["cwd"] != s.options.CWD || !s.workspaceRoots(environment["runtimeWorkspaceRoots"]) {
			return ErrPolicy
		}
	}
	if thread["extra"] != nil {
		return ErrPolicy
	}
	if value, present := thread["canAcceptDirectInput"]; present && value != true {
		return ErrPolicy
	}
	if value := thread["daybreakEnabled"]; value != nil && value != false {
		return ErrPolicy
	}
	for _, key := range []string{"agentRole", "agentNickname", "parentThreadId", "forkedFromId"} {
		if thread[key] != nil {
			return ErrPolicy
		}
	}
	if source := obj(thread["source"]); source != nil {
		if source["subAgent"] != nil {
			return ErrPolicy
		}
	}
	if value := thread["model"]; value != nil && model != "" && value != model {
		return ErrPolicy
	}
	if value := thread["reasoningEffort"]; value != nil && s.effort != nil && value != s.effort {
		return ErrPolicy
	}
	// Both the matched response and early/late notifications enter this same
	// check. Neither may replace the other's persistence identity.
	persistence := [2]string{str(thread["path"]), str(thread["historyMode"])}
	if s.threadPersistence != nil && *s.threadPersistence != persistence {
		return ErrProtocol
	}
	s.threadPersistence = &persistence
	return nil
}
func (s *sessionState) bindTurn(response map[string]any) error {
	view, err := s.turn(obj(response["turn"]))
	if err != nil {
		return err
	}
	s.failureBranch("response", "turn_binding")
	if !s.callbackDone || !validScalar(view.id) || s.provisionalTurn != "" && s.provisionalTurn != view.id {
		return ErrProtocol
	}
	if view.status != "inProgress" {
		if s.terminal == nil || s.terminal.hash != view.hash {
			return ErrProtocol
		}
	}
	if view.hasError {
		return ErrNativeFailure
	}
	s.out.TurnID = view.id
	s.earlyEvents = 0
	s.earlyBytes = 0
	if s.permission {
		return ErrPermission
	}
	// A start response is an in-progress view, not a completed item snapshot.
	if view.status == "inProgress" {
		for _, item := range view.items {
			// The reply snapshot can precede an already-consumed completed
			// notification. It cannot overwrite that final content or authority.
			if old := s.items[item.id]; old != nil && old.completed {
				s.failureBranch("item", "lifecycle")
				s.failureItem(item.kind)
				if old.kind != item.kind {
					return ErrProtocol
				}
				continue
			}
			if err = s.item(item, false); err != nil {
				return err
			}
			s.failureBranch("response", "turn_binding")
		}
	}
	return nil
}

var informational = map[string]bool{
	"thread/name/updated": true, "thread/tokenUsage/updated": true, "account/rateLimits/updated": true,
	"turn/diff/updated": true, "turn/plan/updated": true, "item/agentMessage/delta": true,
	"item/plan/delta": true, "item/reasoning/summaryTextDelta": true, "item/reasoning/summaryPartAdded": true,
	"item/reasoning/textDelta": true, "item/commandExecution/outputDelta": true, "item/commandExecution/terminalInteraction": true,
	"item/fileChange/outputDelta": true, "item/fileChange/patchUpdated": true, "thread/compacted": true,
	"model/verification": true, "turn/moderationMetadata": true, "model/safetyBuffering/updated": true,
	"warning": true, "deprecationNotice": true,
}

func (s *sessionState) event(event codexrpc.Event) error {
	s.noteFailure("event", "envelope", "")
	if (event.Request == nil) == (event.Notification == nil) {
		return ErrProtocol
	}
	if event.Request != nil {
		return s.request(event.Request)
	}
	n := event.Notification
	s.noteFailure("notification", "shape", n.Method)
	s.failure.Shape = FailureFailed
	value, err := s.shapes.object("notification:"+n.Method, n.Params)
	if err != nil {
		return err
	}
	s.failure.Shape = FailurePassed
	// Paginated resume reports display-only usage for the preceding turn. It
	// does not supply the current/provisional turn or bypass early-state limits.
	restoredUsage := n.Method == "thread/tokenUsage/updated" && s.options.ResumeThreadID != "" && value["threadId"] == s.options.ResumeThreadID && s.out.TurnID == "" && (s.phase == "thread/resume" || s.phase == "binding" || s.phase == "turn/start")
	if s.out.Binding.ThreadID == "" || s.out.TurnID == "" && s.phase == "turn/start" || restoredUsage {
		s.earlyEvents++
		s.earlyBytes += len(n.Params)
		if s.earlyEvents > maxEarlyEvents || s.earlyBytes > maxEarlyBytes {
			s.failure.Branch = "early_limit"
			return ErrStateLimit
		}
	}
	if restoredUsage {
		s.failure.Branch, s.failure.Scope = "scope", FailureFailed
		if err := s.threadID(str(value["threadId"])); err != nil {
			return err
		}
		if !validScalar(str(value["turnId"])) {
			return ErrProtocol
		}
		s.failure.Scope = FailurePassed
		return nil
	}
	s.failure.Branch, s.failure.Scope = "scope", FailureFailed
	if err = s.scope(value); err != nil {
		return err
	}
	s.failure.Scope, s.failure.Branch = FailurePassed, "validation"
	if informational[n.Method] {
		return nil
	}
	switch n.Method {
	case "remoteControl/status/changed":
		if value["status"] != "disabled" || value["environmentId"] != nil {
			return ErrPolicy
		}
		// This reports a disabled feature, never identity or RPC authority.
		// Required types come from the pinned shape; empty strings are legal.
		for _, key := range []string{"installationId", "serverName"} {
			text := str(value[key])
			if len(text) > codexrpc.MaxScalarBytes || !utf8.ValidString(text) {
				return ErrStateLimit
			}
		}
	case "thread/started":
		if s.phase != "thread/start" && s.phase != "thread/resume" && s.out.Binding.ThreadID == "" {
			return ErrProtocol
		}
		thread := obj(value["thread"])
		model := str(s.model)
		if s.out.Binding.Model != "" {
			model = s.out.Binding.Model
		}
		if err = s.thread(thread, model); err != nil {
			return err
		}
		s.failure.Branch, s.failure.Scope = "scope", FailureFailed
		if err = s.threadID(str(thread["id"])); err != nil {
			return err
		}
		s.failure.Scope, s.failure.Branch = FailurePassed, "validation"
		hash := digest(thread)
		if s.threadStarted != nil && *s.threadStarted != hash {
			return ErrProtocol
		}
		s.threadStarted = &hash
	case "thread/status/changed":
		status := obj(value["status"])
		switch status["type"] {
		case "idle":
		case "active":
			if !s.callbackDone || s.phase != "turn/start" && s.phase != "running" {
				return ErrProtocol
			}
			for _, flag := range arr(status["activeFlags"]) {
				if flag == "waitingOnUserInput" {
					return ErrProtocol
				}
				if flag == "waitingOnApproval" {
					s.permission = true
				}
			}
		default:
			return ErrNativeFailure
		}
	case "thread/settings/updated":
		return s.settings(obj(value["threadSettings"]))
	case "serverRequest/resolved":
		if s.out.Binding.ThreadID == "" || !s.requests[rpcID(value["requestId"])] {
			return ErrProtocol
		}
	case "error":
		return ErrNativeFailure
	case "turn/started", "turn/completed":
		view, err := s.turn(obj(value["turn"]))
		if err != nil {
			return err
		}
		s.failureBranch("notification", "scope")
		s.failure.Scope = FailureFailed
		if err = s.turnID(view.id); err != nil {
			return err
		}
		s.failure.Scope = FailurePassed
		s.failureBranch("turn", "lifecycle")
		if n.Method == "turn/started" {
			if view.status != "inProgress" || view.hasError {
				return ErrProtocol
			}
			if s.terminal != nil {
				if s.startedTurn != nil && *s.startedTurn == view.hash {
					return nil
				}
				return ErrProtocol
			}
			if s.startedTurn != nil && *s.startedTurn != view.hash {
				return ErrProtocol
			}
			s.startedTurn = &view.hash
			for _, item := range view.items {
				if err = s.item(item, false); err != nil {
					return err
				}
			}
		} else {
			return s.complete(view)
		}
	case "item/started", "item/completed":
		item, err := s.itemView(obj(value["item"]))
		if err != nil {
			return err
		}
		return s.item(item, n.Method == "item/completed")
	default:
		return ErrProtocol
	}
	return nil
}
func (s *sessionState) scope(value map[string]any) error {
	if value["threadId"] != nil {
		if err := s.threadID(str(value["threadId"])); err != nil {
			return err
		}
	}
	if value["turnId"] != nil {
		if err := s.turnID(str(value["turnId"])); err != nil {
			return err
		}
	}
	if value["itemId"] != nil {
		if !validScalar(str(value["itemId"])) {
			return ErrProtocol
		}
	}
	return nil
}
func (s *sessionState) threadID(id string) error {
	if !validScalar(id) {
		return ErrProtocol
	}
	if s.out.Binding.ThreadID != "" {
		if s.out.Binding.ThreadID != id {
			return ErrProtocol
		}
		return nil
	}
	if s.phase != "thread/start" && s.phase != "thread/resume" {
		return ErrProtocol
	}
	if s.options.ResumeThreadID != "" && s.options.ResumeThreadID != id || s.provisionalThread != "" && s.provisionalThread != id {
		return ErrProtocol
	}
	s.provisionalThread = id
	return nil
}
func (s *sessionState) turnID(id string) error {
	if !validScalar(id) || s.out.Binding.ThreadID == "" || !s.callbackDone {
		return ErrProtocol
	}
	if s.out.TurnID != "" {
		if s.out.TurnID != id {
			return ErrProtocol
		}
		return nil
	}
	if s.phase != "turn/start" || s.provisionalTurn != "" && s.provisionalTurn != id {
		return ErrProtocol
	}
	s.provisionalTurn = id
	return nil
}
func (s *sessionState) settings(value map[string]any) error {
	if s.out.Binding.ThreadID == "" || !s.policy(value, "sandboxPolicy") || value["model"] != s.out.Binding.Model || value["modelProvider"] != "openai" || value["effort"] != s.effort || value["serviceTier"] != s.tier {
		return ErrPolicy
	}
	if value["activePermissionProfile"] != nil || value["summary"] != nil || value["personality"] != nil {
		return ErrPolicy
	}
	mode := obj(value["collaborationMode"])
	settings := obj(mode["settings"])
	if mode["mode"] != "default" || settings["model"] != s.out.Binding.Model || settings["reasoning_effort"] != s.effort || settings["developer_instructions"] != nil {
		return ErrPolicy
	}
	return nil
}
func (s *sessionState) request(request *codexrpc.Request) error {
	s.noteFailure("request", "shape", request.Method())
	s.failure.Shape = FailureFailed
	value, err := s.shapes.object("request:"+request.Method(), request.Params())
	if err != nil {
		return err
	}
	s.failure.Shape, s.failure.Branch = FailurePassed, "phase"
	if s.out.Binding.ThreadID == "" || !s.callbackDone || s.phase != "turn/start" && s.phase != "running" || s.terminal != nil {
		return ErrProtocol
	}
	s.failure.Branch, s.failure.Scope = "scope", FailureFailed
	if err = s.scope(value); err != nil {
		return err
	}
	s.failure.Scope, s.failure.Branch = FailurePassed, "validation"
	if value["cwd"] != nil && value["cwd"] != s.options.CWD {
		return ErrPolicy
	}
	if s.out.TurnID == "" {
		s.earlyEvents++
		s.earlyBytes += len(request.Params())
		if s.earlyEvents > maxEarlyEvents || s.earlyBytes > maxEarlyBytes {
			s.failure.Branch = "early_limit"
			return ErrStateLimit
		}
	}
	var reply any
	switch request.Method() {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		reply = map[string]any{"decision": "decline"}
	case "item/permissions/requestApproval":
		reply = map[string]any{"permissions": map[string]any{}, "scope": "turn"}
	default:
		return ErrProtocol
	}
	id, err := request.ID().MarshalJSON()
	if err != nil {
		return ErrProtocol
	}
	if len(s.requests) >= codexrpc.MaxConsumedRequests {
		return ErrStateLimit
	}
	key := string(id)
	if s.requests[key] {
		return ErrProtocol
	}
	s.requests[key] = true
	raw, _ := json.Marshal(reply)
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	s.failure.Branch = "denial_write"
	if s.client.Respond(s.ctx, request, codexrpc.Reply{Result: raw}) != nil {
		if s.ctx.Err() != nil {
			return s.ctx.Err()
		}
		return ErrProtocol
	}
	s.permission = true
	s.failure.Branch = "permission"
	if s.out.TurnID != "" {
		return ErrPermission
	}
	return nil
}
func (s *sessionState) itemView(value map[string]any) (itemView, error) {
	s.failureBranch("item", "view")
	view := itemView{id: str(value["id"]), kind: str(value["type"]), hash: digest(value)}
	s.failureItem(view.kind)
	if !validScalar(view.id) {
		return view, ErrProtocol
	}
	switch view.kind {
	case "agentMessage":
		if value["delivery"] != nil || len(arr(value["questions"])) != 0 {
			return view, ErrOutput
		}
		view.phase = str(value["phase"])
		if view.phase != "commentary" {
			view.text = str(value["text"])
			if len(view.text) > maxFinalBytes {
				return view, ErrStateLimit
			}
		}
	case "userMessage", "reasoning", "plan", "commandExecution", "fileChange", "imageView", "contextCompaction":
		if value["cwd"] != nil && value["cwd"] != s.options.CWD {
			return view, ErrPolicy
		}
		if value["pluginId"] != nil || value["scriptPath"] != nil {
			return view, ErrPolicy
		}
	default:
		return view, ErrProtocol
	}
	return view, nil
}
func (s *sessionState) item(view itemView, completed bool) error {
	s.failureBranch("item", "lifecycle")
	s.failureItem(view.kind)
	existing := s.items[view.id]
	if existing != nil {
		if existing.kind != view.kind {
			return ErrProtocol
		}
		if existing.completed {
			if !completed || existing.hash != view.hash {
				return ErrProtocol
			}
			return nil
		}
		if !completed {
			if existing.hash != view.hash {
				return ErrProtocol
			}
			return nil
		}
	} else {
		if s.terminal != nil {
			return ErrProtocol
		}
		if len(s.items) >= maxItems {
			return ErrStateLimit
		}
	}
	// Started snapshots can change while streaming; retain their hash only.
	if !completed {
		view.text = ""
	} else {
		s.finalBytes += len(view.text)
		if s.finalBytes > maxFinalBytes {
			return ErrStateLimit
		}
	}
	view.completed = completed
	s.items[view.id] = &view
	return nil
}
func (s *sessionState) turn(value map[string]any) (turnView, error) {
	s.failureBranch("turn", "view")
	view := turnView{id: str(value["id"]), status: str(value["status"]), view: str(value["itemsView"]), hash: digest(value), hasError: value["error"] != nil}
	if !validScalar(view.id) {
		return view, ErrProtocol
	}
	if view.view == "" {
		view.view = "full"
	}
	if view.view == "notLoaded" && len(arr(value["items"])) != 0 {
		return view, ErrProtocol
	}
	if len(arr(value["items"])) > maxItems {
		return view, ErrStateLimit
	}
	textBytes := 0
	seen := make(map[string]bool)
	for _, raw := range arr(value["items"]) {
		item, err := s.itemView(obj(raw))
		if err != nil {
			return view, err
		}
		s.failureBranch("turn", "view")
		if seen[item.id] {
			return view, ErrProtocol
		}
		seen[item.id] = true
		textBytes += len(item.text)
		if textBytes > maxFinalBytes {
			return view, ErrStateLimit
		}
		view.items = append(view.items, item)
	}
	return view, nil
}
func (s *sessionState) complete(view turnView) error {
	s.failureBranch("turn", "complete")
	if view.status == "inProgress" || view.status == "completed" && view.hasError || view.status == "failed" && !view.hasError || view.status == "interrupted" && view.hasError {
		return ErrProtocol
	}
	if s.terminal != nil {
		if s.terminal.hash != view.hash {
			return ErrProtocol
		}
		return nil
	}
	if view.view == "full" {
		seen := make(map[string]bool)
		for _, item := range view.items {
			seen[item.id] = true
			if err := s.item(item, true); err != nil {
				return err
			}
			s.failureBranch("turn", "complete")
		}
		for id, item := range s.items {
			if item.completed && !seen[id] {
				return ErrProtocol
			}
			if !item.completed {
				return ErrProtocol
			}
		}
	}
	// Partial summaries are display-only. Known identity/type must agree, but
	// their text cannot replace, complete or promote the retained full snapshot.
	if view.view != "full" {
		for _, item := range view.items {
			old := s.items[item.id]
			if old != nil {
				if old.kind != item.kind {
					return ErrProtocol
				}
				continue
			}
			if item.kind == "agentMessage" && item.phase != "commentary" {
				return ErrOutput
			}
		}
	}
	view.items = nil // final text has exactly one retained owner in s.items.
	s.terminal = &view
	s.out.Status = view.status
	return nil
}
func (s *sessionState) finish() (Outcome, error) {
	s.noteFailure("finish", "terminal_binding", "")
	if s.terminal == nil || s.out.TurnID == "" || s.terminal.id != s.out.TurnID {
		return s.out, ErrProtocol
	}
	s.failure.Branch = "permission"
	if s.permission {
		return s.out, ErrPermission
	}
	s.failure.Branch = "native_status"
	if s.terminal.status != "completed" {
		return s.out, ErrNativeFailure
	}
	var candidate *itemView
	s.failure.Branch = "final_selection"
	for _, item := range s.items {
		if !item.completed && item.kind == "agentMessage" && item.phase != "commentary" {
			return s.out, ErrOutput
		}
		if !item.completed || item.kind != "agentMessage" || item.phase == "commentary" {
			continue
		}
		if candidate != nil {
			return s.out, ErrOutput
		}
		candidate = item
	}
	if candidate == nil {
		return s.out, ErrOutput
	}
	s.failure.Branch = "decode_final"
	completion, err := adapter.DecodeTaskCompletion(candidate.text, maxFinalBytes)
	if err != nil {
		return s.out, ErrOutput
	}
	s.out.Completion = completion
	return s.out, nil
}
func obj(value any) map[string]any { object, _ := value.(map[string]any); return object }
func arr(value any) []any          { array, _ := value.([]any); return array }
func str(value any) string         { text, _ := value.(string); return text }
func digest(value any) [32]byte    { raw, _ := json.Marshal(value); return sha256.Sum256(raw) }
func rpcID(value any) string {
	if n, ok := value.(json.Number); ok {
		integer, err := strconv.ParseInt(string(n), 10, 64)
		if err != nil {
			return ""
		}
		return strconv.FormatInt(integer, 10)
	}
	if text, ok := value.(string); ok {
		raw, _ := json.Marshal(text)
		return string(raw)
	}
	return ""
}
