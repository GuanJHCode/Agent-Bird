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
	ControllerThread string `json:"controller_thread"`
	OriginPID        int    `json:"origin_pid"`
	OriginBirth      string `json:"origin_birth"`
}

func (s *Server) bindLocalOwner(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var req OwnerBindRequest
	if err := strictJSON(raw, &req); err != nil {
		return nil, err
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
	return json.Marshal(map[string]any{"version": 1, "owner_mode": "local", "owner_capability": path, "controller_thread": g.ControllerThread, "origin_pid": g.OriginPID, "origin_birth": g.OriginBirth, "origin_context_id": g.OriginContextID, "host_generation": g.HostGeneration})
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
