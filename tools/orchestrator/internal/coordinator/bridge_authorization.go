package coordinator

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/nativebridgecli"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type bridgeControlCapability struct {
	Version          int    `json:"version"`
	RunID            string `json:"run_id"`
	ControllerThread string `json:"controller_thread"`
	ControlToken     string `json:"control_token"`
	BootstrapPath    string `json:"bootstrap_path"`
	HostLaunchID     string `json:"host_launch_id,omitempty"`
	HostLaunchToken  string `json:"host_launch_token,omitempty"`
}

func (s *Server) readBridgeControl(path string) (bridgeControlCapability, error) {
	var cap bridgeControlCapability
	root, err := filepath.EvalSymlinks(s.stateDir)
	if err != nil {
		return cap, err
	}
	dir := filepath.Join(root, "control")
	canonical, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil || canonical != dir || filepath.Dir(path) != dir {
		return cap, store.CodeError("control_file_invalid")
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode().Perm() != 0700 {
		return cap, store.CodeError("control_file_invalid")
	}
	dirStat, ok := dirInfo.Sys().(*syscall.Stat_t)
	if !ok || dirStat.Uid != uint32(os.Geteuid()) {
		return cap, store.CodeError("control_file_invalid")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return cap, store.CodeError("control_file_invalid")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() <= 0 || info.Size() > 8192 {
		return cap, store.CodeError("control_file_invalid")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return cap, store.CodeError("control_file_invalid")
	}
	raw := make([]byte, info.Size())
	if _, err = io.ReadFull(io.NewSectionReader(f, 0, info.Size()), raw); err != nil {
		return cap, store.CodeError("control_file_invalid")
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) {
		return cap, store.CodeError("control_file_invalid")
	}
	if err = contract.DecodeBridgeStrict(raw, &cap); err != nil || (cap.Version != 1 && cap.Version != 2) || cap.RunID == "" || cap.ControllerThread == "" || cap.ControlToken == "" || !filepath.IsAbs(cap.BootstrapPath) {
		return bridgeControlCapability{}, store.CodeError("control_file_invalid")
	}
	if cap.Version == 2 && (cap.HostLaunchID == "" || cap.HostLaunchToken == "") {
		return bridgeControlCapability{}, store.CodeError("control_file_invalid")
	}
	return cap, nil
}

// AuthorizeBridgeOwner is the reusable B04 entry after a typed B01 decoder.
// Its returned context carries private facts; request JSON cannot mint them.
func (s *Server) AuthorizeBridgeOwner(ctx context.Context, conn net.Conn, runID, mode, ownerFile, controlFile string) (context.Context, contract.BridgeVerifiedPeer, error) {
	var peer contract.BridgeVerifiedPeer
	ctx, err := s.authorizePeer(ctx, conn)
	if err != nil {
		return ctx, peer, err
	}
	pid, err := ipc.PeerPID(conn)
	if err != nil {
		return ctx, peer, err
	}
	ancestry, _ := ctx.Value(peerAncestryKey{}).(map[int]string)
	birth := ancestry[pid]
	if birth == "" {
		return ctx, peer, store.CodeError("owner_peer_mismatch")
	}
	cap, err := s.readBridgeControl(controlFile)
	if err != nil {
		return ctx, peer, err
	}
	if cap.RunID != runID {
		return ctx, peer, store.CodeError("owner_mismatch")
	}
	proof := store.BridgeOwnerProof{RunID: runID, OwnerMode: mode, ControllerThread: cap.ControllerThread, ControlToken: cap.ControlToken, PeerPID: pid, PeerBirth: birth}
	if mode == "local" || mode == "managed" {
		grant, err := s.localOwner(ctx, ownerFile)
		if err != nil {
			return ctx, peer, err
		}
		proof.OwnerContextID, proof.OwnerPID, proof.OwnerBirth = grant.OriginContextID, grant.OriginPID, grant.OriginBirth
		proof.OwnerGrantID, proof.OwnerGrantToken = grant.ID, grant.Token
		if grant.ControllerThread != cap.ControllerThread {
			return ctx, peer, store.CodeError("owner_capability_mismatch")
		}
	} else if mode == "native" {
		owner, err := nativebridgecli.VerifyOwnerCapability(ownerFile)
		if err != nil {
			return ctx, peer, store.CodeError("owner_capability_invalid")
		}
		if owner.ControllerThread != cap.ControllerThread || ancestry[owner.OriginPID] != owner.OriginBirth {
			return ctx, peer, store.CodeError("owner_peer_mismatch")
		}
		proof.OwnerContextID, proof.OwnerPID, proof.OwnerBirth = owner.OriginContextID, owner.OriginPID, owner.OriginBirth
		proof.NativeOwnerCapabilityPath = owner.OwnerCapability
		proof.NativeHostGeneration = owner.HostGeneration
		proof.NativeAttachmentProofSHA256 = owner.AttachmentProofSHA256
	} else {
		return ctx, peer, store.CodeError("owner_mode_invalid")
	}
	binding, err := s.db.ValidateRunOwner(ctx, runID, cap.ControllerThread, cap.ControlToken)
	if err != nil {
		return ctx, peer, err
	}
	if binding.OriginContextID != proof.OwnerContextID || binding.OriginPID != proof.OwnerPID || binding.OriginBirth != proof.OwnerBirth {
		return ctx, peer, store.CodeError("owner_mismatch")
	}
	peer = contract.BridgeVerifiedPeer{UID: os.Geteuid(), PID: pid, Birth: birth, OwnerContextID: proof.OwnerContextID}
	return store.WithBridgeOwnerProof(ctx, proof), peer, nil
}

func (s *Server) AuthorizeBridgeBind(ctx context.Context, conn net.Conn, raw json.RawMessage) (context.Context, contract.BridgeBindRequest, error) {
	req, err := contract.DecodeBridgeBindRequest(raw)
	if err != nil {
		return ctx, req, err
	}
	ctx, peer, err := s.AuthorizeBridgeOwner(ctx, conn, req.RunID, req.OwnerMode, req.OwnerCapabilityFile, req.ControlFile)
	if err != nil {
		return ctx, req, err
	}
	ancestry, err := process.AncestryContext(ctx, req.NativePID)
	if err != nil || ancestry[req.NativePID] != req.NativeBirth {
		return ctx, req, store.CodeError("bridge_sender_unknown")
	}
	owner, ok := store.BridgeOwnerProofFromContext(ctx)
	if !ok || ancestry[owner.OwnerPID] != owner.OwnerBirth || req.NativePID == owner.OwnerPID {
		return ctx, req, store.CodeError("owner_peer_mismatch")
	}
	worker, err := s.db.IsWorkerAncestry(ctx, ancestry)
	if err != nil {
		return ctx, req, err
	}
	if worker {
		return ctx, req, store.CodeError("worker_dispatch_forbidden")
	}
	exe, err := process.ExecutablePath(req.NativePID)
	if err != nil {
		return ctx, req, store.CodeError("bridge_sender_unknown")
	}
	digest, err := process.ExecutableDigest(ctx, exe)
	if err != nil || digest != req.NativeExecutableSHA256 {
		return ctx, req, store.CodeError("bridge_sender_unknown")
	}
	req.Peer = peer
	return ctx, req, nil
}

func (s *Server) AuthorizeBridgeActivate(ctx context.Context, conn net.Conn, raw json.RawMessage) (context.Context, contract.BridgeActivateRequest, error) {
	req, err := contract.DecodeBridgeActivateRequest(raw)
	if err != nil {
		return ctx, req, err
	}
	b, err := s.db.BridgeBindingIdentity(ctx, req.BindingID)
	if err != nil {
		return ctx, req, err
	}
	ctx, peer, err := s.AuthorizeBridgeOwner(ctx, conn, b.RunID, req.OwnerMode, req.OwnerCapabilityFile, req.ControlFile)
	if err != nil {
		return ctx, req, err
	}
	owner, ok := store.BridgeOwnerProofFromContext(ctx)
	if !ok || b.OwnerEpoch != req.OwnerEpoch || b.OwnerContextID != owner.OwnerContextID || b.OwnerPID != owner.OwnerPID || b.OwnerBirth != owner.OwnerBirth || (b.Status != "SESSION_OBSERVED" && b.Status != "ACTIVE") {
		return ctx, req, store.CodeError("bridge_no_active_binding")
	}
	req.Peer = peer
	return ctx, req, nil
}

// AuthorizeBridgeHook is reusable for B03 Claim; Ingest uses its typed wrapper.
func (s *Server) AuthorizeBridgeHook(ctx context.Context, conn net.Conn, bindingID, token string) (context.Context, contract.BridgeVerifiedPeer, error) {
	var peer contract.BridgeVerifiedPeer
	ctx, err := s.authorizePeer(ctx, conn)
	if err != nil {
		return ctx, peer, err
	}
	pid, err := ipc.PeerPID(conn)
	if err != nil {
		return ctx, peer, err
	}
	ancestry, _ := ctx.Value(peerAncestryKey{}).(map[int]string)
	b, err := s.db.BridgeBindingIdentity(ctx, bindingID)
	if err != nil {
		return ctx, peer, err
	}
	if b.Status == "STALE" {
		return ctx, peer, store.CodeError("bridge_no_active_binding")
	}
	if ancestry[b.NativePID] != b.NativeBirth || ancestry[pid] == "" {
		return ctx, peer, store.CodeError("bridge_sender_unknown")
	}
	peer = contract.BridgeVerifiedPeer{UID: os.Geteuid(), PID: pid, Birth: ancestry[pid], OwnerContextID: b.OwnerContextID}
	return store.WithBridgeHookProof(ctx, store.BridgeHookProof{BindingID: bindingID, HookToken: token, PeerPID: pid, PeerBirth: peer.Birth}), peer, nil
}

func (s *Server) AuthorizeBridgeIngest(ctx context.Context, conn net.Conn, raw json.RawMessage) (context.Context, contract.BridgeIngestRequest, error) {
	req, err := contract.DecodeBridgeIngestRequest(raw)
	if err != nil {
		return ctx, req, err
	}
	ctx, peer, err := s.AuthorizeBridgeHook(ctx, conn, req.BindingID, req.HookCapability)
	if err != nil {
		return ctx, req, err
	}
	req.Peer = peer
	return ctx, req, nil
}

func (s *Server) AuthorizeBridgeUnbind(ctx context.Context, conn net.Conn, raw json.RawMessage) (context.Context, contract.BridgeUnbindRequest, error) {
	req, err := contract.DecodeBridgeUnbindRequest(raw)
	if err != nil {
		return ctx, req, err
	}
	b, err := s.db.BridgeBindingIdentity(ctx, req.BindingID)
	if err != nil {
		return ctx, req, err
	}
	ctx, peer, err := s.AuthorizeBridgeOwner(ctx, conn, b.RunID, req.OwnerMode, req.OwnerCapabilityFile, req.ControlFile)
	if err != nil {
		return ctx, req, err
	}
	owner, ok := store.BridgeOwnerProofFromContext(ctx)
	if !ok || b.Status == "STALE" || b.OwnerEpoch != req.OwnerEpoch || b.OwnerPID != owner.OwnerPID || b.OwnerBirth != owner.OwnerBirth || b.OwnerContextID != owner.OwnerContextID {
		return ctx, req, store.CodeError("bridge_no_active_binding")
	}
	req.Peer = peer
	return ctx, req, nil
}

// This predicate is intentionally private. Only the three typed operation
// wrappers below can select historical owner access; JSON has no operation bit.
func (s *Server) authorizeBridgeHistoricalOwner(ctx context.Context, conn net.Conn, bindingID string, epoch uint64, mode, ownerFile, controlFile string) (context.Context, contract.BridgeVerifiedPeer, error) {
	var peer contract.BridgeVerifiedPeer
	b, err := s.db.BridgeBindingIdentity(ctx, bindingID)
	if err != nil {
		return ctx, peer, err
	}
	if b.OwnerEpoch != epoch {
		return ctx, peer, store.CodeError("invalid_epoch")
	}
	ctx, peer, err = s.AuthorizeBridgeOwner(ctx, conn, b.RunID, mode, ownerFile, controlFile)
	if err != nil {
		return ctx, peer, err
	}
	owner, ok := store.BridgeOwnerProofFromContext(ctx)
	if !ok || owner.OwnerContextID != b.OwnerContextID {
		return ctx, peer, store.CodeError("owner_mismatch")
	}
	if b.Status != "STALE" && b.Status != "DETACHED" && (b.OwnerPID != owner.OwnerPID || b.OwnerBirth != owner.OwnerBirth) {
		return ctx, peer, store.CodeError("owner_mismatch")
	}
	return ctx, peer, nil
}

func (s *Server) AuthorizeBridgeStatus(ctx context.Context, conn net.Conn, raw json.RawMessage) (context.Context, contract.BridgeStatusRequest, error) {
	req, err := contract.DecodeBridgeStatusRequest(raw)
	if err != nil {
		return ctx, req, err
	}
	ctx, peer, err := s.authorizeBridgeHistoricalOwner(ctx, conn, req.BindingID, req.OwnerEpoch, req.OwnerMode, req.OwnerCapabilityFile, req.ControlFile)
	if err != nil {
		return ctx, req, err
	}
	req.Peer = peer
	return ctx, req, nil
}

func (s *Server) AuthorizeBridgeBatchQuery(ctx context.Context, conn net.Conn, raw json.RawMessage) (context.Context, contract.BridgeReceiveRequest, error) {
	req, err := contract.DecodeBridgeReceiveRequest(raw)
	if err != nil {
		return ctx, req, err
	}
	ctx, peer, err := s.authorizeBridgeHistoricalOwner(ctx, conn, req.BindingID, req.OwnerEpoch, req.OwnerMode, req.OwnerCapabilityFile, req.ControlFile)
	if err != nil {
		return ctx, req, err
	}
	req.Peer = peer
	return ctx, req, nil
}

func (s *Server) AuthorizeBridgeReceived(ctx context.Context, conn net.Conn, raw json.RawMessage) (context.Context, contract.BridgeReceiveRequest, error) {
	req, err := contract.DecodeBridgeReceiveRequest(raw)
	if err != nil {
		return ctx, req, err
	}
	ctx, peer, err := s.authorizeBridgeHistoricalOwner(ctx, conn, req.BindingID, req.OwnerEpoch, req.OwnerMode, req.OwnerCapabilityFile, req.ControlFile)
	if err != nil {
		return ctx, req, err
	}
	req.Peer = peer
	return ctx, req, nil
}

func (s *Server) AuthorizeBridgeRedeliver(ctx context.Context, conn net.Conn, raw json.RawMessage) (context.Context, contract.BridgeRedeliverRequest, error) {
	req, err := contract.DecodeBridgeRedeliverRequest(raw)
	if err != nil {
		return ctx, req, err
	}
	b, err := s.db.BridgeBindingIdentity(ctx, req.BindingID)
	if err != nil {
		return ctx, req, err
	}
	ctx, peer, err := s.AuthorizeBridgeOwner(ctx, conn, b.RunID, req.OwnerMode, req.OwnerCapabilityFile, req.ControlFile)
	if err != nil {
		return ctx, req, err
	}
	owner, ok := store.BridgeOwnerProofFromContext(ctx)
	if !ok || b.Status != "ACTIVE" || b.OwnerEpoch != req.OwnerEpoch || b.OwnerContextID != owner.OwnerContextID || b.OwnerPID != owner.OwnerPID || b.OwnerBirth != owner.OwnerBirth {
		return ctx, req, store.CodeError("bridge_no_active_binding")
	}
	req.Peer = peer
	return ctx, req, nil
}
