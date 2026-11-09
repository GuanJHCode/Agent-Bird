package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
)

const mcpCLIMaxBytes = 128 * 1024
const mcpResultMaxBytes = 256 * 1024

type mcpFacade struct {
	path   string
	config mcpConfiguration
	key    []byte
	invoke func(context.Context, []string, io.Writer) error
}

func newMCPFacade(path string) (*mcpFacade, error) {
	config, key, err := loadMCPConfiguration(path)
	if err != nil {
		return nil, err
	}
	return &mcpFacade{path: path, config: config, key: key, invoke: taskEntry}, nil
}
func (f *mcpFacade) checkConfiguration() error {
	current, key, err := loadMCPConfiguration(f.path)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, f.config) || !hmac.Equal(key, f.key) {
		return codeError("mcp_configuration_changed")
	}
	return nil
}

type mcpPacket struct {
	Version   int             `json:"version"`
	Kind      string          `json:"kind"`
	Namespace string          `json:"namespace"`
	Value     json.RawMessage `json:"value"`
}

func (f *mcpFacade) seal(kind string, value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(mcpPacket{1, kind, f.config.Namespace, body})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, f.key)
	_, _ = mac.Write(raw)
	token := base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if len(token) > mcpTokenMaxBytes {
		return "", codeError("mcp_token_too_large")
	}
	return token, nil
}
func (f *mcpFacade) unseal(token, kind string, target any) error {
	if len(token) > mcpTokenMaxBytes {
		return codeError("invalid_mcp_reference")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return codeError("invalid_mcp_reference")
	}
	raw, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return codeError("invalid_mcp_reference")
	}
	signature, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil || len(signature) != sha256.Size {
		return codeError("invalid_mcp_reference")
	}
	mac := hmac.New(sha256.New, f.key)
	_, _ = mac.Write(raw)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return codeError("invalid_mcp_reference")
	}
	var packet mcpPacket
	if e = decode(raw, &packet); e != nil || packet.Version != 1 || packet.Kind != kind {
		return codeError("invalid_mcp_reference")
	}
	if packet.Namespace != f.config.Namespace {
		return codeError("route_namespace_mismatch")
	}
	if e = decode(packet.Value, target); e != nil {
		return codeError("invalid_mcp_reference")
	}
	return nil
}

// A route is immutable routing metadata. Business state and authority are read
// through the original capability/runtime on every operation, never cached here.
type mcpRoute struct {
	ID        string   `json:"id"`
	Handle    string   `json:"handle"`
	StateDir  string   `json:"state_dir"`
	Workspace string   `json:"workspace"`
	RunID     string   `json:"run_id"`
	Owner     string   `json:"owner"`
	TaskIDs   []string `json:"task_ids"`
}

func (f *mcpFacade) routePathAllowed(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	relative, err := filepath.Rel(filepath.Join(f.config.StateDir, "dispatch"), path)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
func (f *mcpFacade) mintRoute(path, owner string) (string, error) {
	if !f.routePathAllowed(path) || owner == "" {
		return "", codeError("mcp_route_scope_mismatch")
	}
	if err := privateHandleParent(path); err != nil {
		return "", err
	}
	var h taskHandle
	if err := mcpReadPrivate(path, mcpConfigMaxBytes, &h); err != nil {
		return "", err
	}
	if h.Version != 1 || h.RunID == "" || h.StateDir != f.config.StateDir || len(h.TaskIDs) == 0 {
		return "", codeError("mcp_route_scope_mismatch")
	}
	route := f.routeValue(path, owner, h)
	if h.ControlFile != "" {
		if _, err := f.loadRouteControl(route, h); err != nil {
			return "", err
		}
	}
	if coordinates, ok := f.routeCoordinates(path); ok {
		return f.compactRoute(route, coordinates)
	}
	// Historical noncanonical dispatch layouts retain their sealed references.
	return f.seal("route", route)
}

func (f *mcpFacade) routeValue(path, owner string, h taskHandle) mcpRoute {
	return mcpRoute{ID: dispatchHash(f.config.Namespace + "\x00" + path + "\x00" + h.RunID + "\x00" + owner), Handle: path, StateDir: h.StateDir, Workspace: f.config.Workspace, RunID: h.RunID, Owner: owner, TaskIDs: append([]string(nil), h.TaskIDs...)}
}
func (f *mcpFacade) loadRouteControl(route mcpRoute, h taskHandle) (controlCapability, error) {
	var empty controlCapability
	if h.ControlFile == "" {
		return empty, codeError("task_handle_unresolved")
	}
	cap, err := loadControlCapability(h.ControlFile)
	if err != nil {
		return empty, err
	}
	if cap.RunID != route.RunID || cap.ControllerThread != route.Owner {
		return empty, codeError("mcp_route_scope_mismatch")
	}
	bootstrap, err := loadHostBootstrap(cap)
	if err != nil {
		return empty, err
	}
	if bootstrap.SocketPath != filepath.Join(route.StateDir, "coordinator.sock") {
		return empty, codeError("mcp_route_scope_mismatch")
	}
	return cap, nil
}
func (f *mcpFacade) resolveRoute(token string) (mcpRoute, taskHandle, controlCapability, error) {
	var route mcpRoute
	var h taskHandle
	var cap controlCapability
	if err := f.checkConfiguration(); err != nil {
		return route, h, cap, err
	}
	var decodeErr error
	if strings.HasPrefix(token, mcpCompactRoutePrefix) {
		route, decodeErr = f.openCompactRoute(token)
	} else {
		decodeErr = f.unseal(token, "route", &route)
	}
	if decodeErr != nil {
		return route, h, cap, decodeErr
	}
	if route.Owner == "" || route.RunID == "" || len(route.TaskIDs) == 0 || route.StateDir != f.config.StateDir || route.Workspace != f.config.Workspace || !f.routePathAllowed(route.Handle) || route.ID != dispatchHash(f.config.Namespace+"\x00"+route.Handle+"\x00"+route.RunID+"\x00"+route.Owner) {
		return route, h, cap, codeError("mcp_route_scope_mismatch")
	}
	if err := privateHandleParent(route.Handle); err != nil {
		return route, h, cap, err
	}
	if err := mcpReadPrivate(route.Handle, mcpConfigMaxBytes, &h); err != nil {
		return route, h, cap, err
	}
	if h.Version != 1 || h.RunID != route.RunID || h.StateDir != route.StateDir || !slices.Equal(h.TaskIDs, route.TaskIDs) {
		return route, h, cap, codeError("mcp_route_scope_mismatch")
	}
	var err error
	cap, err = f.loadRouteControl(route, h)
	return route, h, cap, err
}

type mcpReview struct {
	RouteID string                        `json:"route_id"`
	Owner   string                        `json:"owner"`
	RunID   string                        `json:"run_id"`
	TaskID  string                        `json:"task_id"`
	Binding store.ReviewableResultBinding `json:"binding"`
}

func (f *mcpFacade) reviewReference(route mcpRoute, task string, binding store.ReviewableResultBinding) (string, error) {
	if err := validMCPReview(route, mcpReview{route.ID, route.Owner, route.RunID, task, binding}); err != nil {
		return "", err
	}
	if err := f.checkConfiguration(); err != nil {
		return "", err
	}
	review := mcpReview{route.ID, route.Owner, route.RunID, task, binding}
	if route.Handle != "" {
		return f.compactReview(route, review)
	}
	return f.seal("review", review)
}

func validMCPReview(route mcpRoute, review mcpReview) error {
	if review.RouteID != route.ID || review.Owner != route.Owner || review.RunID != route.RunID || !slices.Contains(route.TaskIDs, review.TaskID) {
		return codeError("mcp_review_scope_mismatch")
	}
	binding := review.Binding
	if binding.CandidateOID == "" || binding.EventID == "" || binding.EventRevision < 1 || binding.EventHash == "" || binding.ActionSlot == "" || binding.SegmentID == "" || binding.WorkRevision < 1 {
		return codeError("candidate_not_reviewable")
	}
	return nil
}
func (f *mcpFacade) resolveReview(token string, route mcpRoute) (mcpReview, error) {
	var review mcpReview
	if err := f.checkConfiguration(); err != nil {
		return review, err
	}
	var err error
	if strings.HasPrefix(token, mcpCompactReviewPrefix) {
		review, err = f.openCompactReview(token, route)
	} else {
		err = f.unseal(token, "review", &review)
	}
	if err != nil {
		return review, err
	}
	return review, validMCPReview(route, review)
}
func mcpTask(route mcpRoute, task string) (string, error) {
	if task == "" && len(route.TaskIDs) == 1 {
		task = route.TaskIDs[0]
	}
	if !slices.Contains(route.TaskIDs, task) {
		return "", codeError("task_handle_scope_mismatch")
	}
	return task, nil
}

type mcpLimitedBuffer struct {
	buffer bytes.Buffer
	limit  int
	err    error
}

func (b *mcpLimitedBuffer) Write(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	if len(p) > b.limit-b.buffer.Len() {
		b.err = codeError("mcp_result_too_large")
		return 0, b.err
	}
	return b.buffer.Write(p)
}
func mcpCapture(call func(io.Writer) error) (map[string]any, error) {
	out := &mcpLimitedBuffer{limit: mcpCLIMaxBytes}
	callErr := call(out)
	if out.err != nil {
		return nil, out.err
	}
	if out.buffer.Len() == 0 {
		if callErr != nil {
			return nil, callErr
		}
		return nil, codeError("mcp_cli_result_invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(out.buffer.Bytes()))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, codeError("mcp_cli_result_invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, codeError("mcp_cli_result_invalid")
	}
	return value, callErr
}
func (f *mcpFacade) cli(ctx context.Context, args []string) (map[string]any, error) {
	return mcpCapture(func(out io.Writer) error { return f.invoke(ctx, args, out) })
}
