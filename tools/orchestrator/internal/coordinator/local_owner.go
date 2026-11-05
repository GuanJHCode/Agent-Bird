package coordinator

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
)

type peerAncestryKey struct{}

func (s *Server) authorizePeer(ctx context.Context, conn net.Conn) (context.Context, error) {
	pid, err := ipc.PeerPID(conn)
	if err != nil {
		return ctx, err
	}
	ancestors, err := process.Ancestry(pid)
	if err != nil {
		return ctx, err
	}
	worker, err := s.db.IsWorkerAncestry(ctx, ancestors)
	if err != nil {
		return ctx, err
	}
	if worker {
		return ctx, store.CodeError("worker_dispatch_forbidden")
	}
	return context.WithValue(ctx, peerAncestryKey{}, ancestors), nil
}

type OwnerBindRequest struct {
	OwnerProjectionVersion int    `json:"owner_projection_version,omitempty"`
	ControllerThread       string `json:"controller_thread"`
	OriginPID              int    `json:"origin_pid"`
	OriginBirth            string `json:"origin_birth"`
}

// OwnerBindResponse describes the local process authority. OwnerScope and
// OwnerCapabilities are informational projections, never capability material.
// A native-looking controller thread label does not prove native chat routing.
type OwnerBindResponse struct {
	Version           int                `json:"version"`
	OwnerMode         string             `json:"owner_mode"`
	OwnerCapability   string             `json:"owner_capability"`
	ControllerThread  string             `json:"controller_thread"`
	OriginPID         int                `json:"origin_pid"`
	OriginBirth       string             `json:"origin_birth"`
	OriginContextID   string             `json:"origin_context_id"`
	HostGeneration    string             `json:"host_generation"`
	OwnerScope        string             `json:"owner_scope,omitempty"`
	OwnerCapabilities *OwnerCapabilities `json:"owner_capabilities,omitempty"`
}

type OwnerCapabilities struct {
	Version            int                           `json:"version"`
	Delivery           OwnerDeliveryCapabilities     `json:"delivery"`
	Continuation       OwnerContinuationCapabilities `json:"continuation"`
	ObservesChatSwitch bool                          `json:"observes_chat_switch"`
	ParentExitPolicy   string                        `json:"parent_exit_policy"`
}

type OwnerDeliveryCapabilities struct {
	Collect    bool `json:"collect"`
	Wait       bool `json:"wait"`
	NativePush bool `json:"native_push"`
}

type OwnerContinuationCapabilities struct {
	FreshInvocation bool `json:"fresh_invocation"`
	TypedResume     bool `json:"typed_resume"`
	ExplicitRebind  bool `json:"explicit_rebind"`
	AutomaticRebind bool `json:"automatic_rebind"`
}

func managedOwnerCapabilities() OwnerCapabilities {
	return OwnerCapabilities{
		Version:          1,
		Delivery:         OwnerDeliveryCapabilities{Collect: true, Wait: true},
		Continuation:     OwnerContinuationCapabilities{FreshInvocation: true, ExplicitRebind: true},
		ParentExitPolicy: "stop_and_retain",
	}
}

func (s *Server) bindLocalOwner(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var req OwnerBindRequest
	if err := strictJSON(raw, &req); err != nil {
		return nil, err
	}
	if req.OwnerProjectionVersion != 0 && req.OwnerProjectionVersion != 1 {
		return nil, store.CodeError("owner_projection_version_unsupported")
	}
	ancestry, _ := ctx.Value(peerAncestryKey{}).(map[int]string)
	if req.OriginBirth == "" || ancestry[req.OriginPID] != req.OriginBirth {
		return nil, store.CodeError("owner_peer_mismatch")
	}
	g, err := s.db.CreateOwnerGrant(ctx, req.ControllerThread, req.OriginPID, req.OriginBirth)
	if err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(s.stateDir)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, "owners")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil || canonical != dir {
		return nil, store.CodeError("owner_path_untrusted")
	}
	path := filepath.Join(dir, g.ID+".json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	err = json.NewEncoder(f).Encode(g)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	d, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	err = d.Sync()
	d.Close()
	if err != nil {
		return nil, err
	}
	response := OwnerBindResponse{
		Version: 1, OwnerMode: "local", OwnerCapability: path,
		ControllerThread: g.ControllerThread, OriginPID: g.OriginPID, OriginBirth: g.OriginBirth,
		OriginContextID: g.OriginContextID, HostGeneration: g.HostGeneration,
	}
	if req.OwnerProjectionVersion == 1 {
		response.OwnerScope = "managed_process"
		capabilities := managedOwnerCapabilities()
		response.OwnerCapabilities = &capabilities
	}
	return json.Marshal(response)
}
func (s *Server) localOwner(ctx context.Context, path string) (store.OwnerGrant, error) {
	var g store.OwnerGrant
	root, err := filepath.EvalSymlinks(s.stateDir)
	if err != nil {
		return g, err
	}
	dir := filepath.Join(root, "owners")
	canonical, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil || canonical != dir || filepath.Dir(path) != dir {
		return g, store.CodeError("owner_path_untrusted")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return g, store.CodeError("owner_capability_invalid")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 8192 {
		return g, store.CodeError("owner_capability_invalid")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return g, store.CodeError("owner_capability_invalid")
	}
	raw := make([]byte, info.Size())
	if _, err = f.ReadAt(raw, 0); err != nil {
		return g, store.CodeError("owner_capability_invalid")
	}
	if err = strictJSON(raw, &g); err != nil {
		return g, store.CodeError("owner_capability_invalid")
	}
	if err = s.db.ValidateOwnerGrant(ctx, g); err != nil {
		return g, err
	}
	ancestry, _ := ctx.Value(peerAncestryKey{}).(map[int]string)
	if ancestry[g.OriginPID] != g.OriginBirth {
		return g, store.CodeError("owner_peer_mismatch")
	}
	return g, nil
}
