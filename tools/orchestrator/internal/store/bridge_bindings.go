package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/nativebridgecli"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// These facts are populated by coordinator from the Unix socket, private
// capability files and native attachment verifier. They are never wire DTOs.
type BridgeOwnerProof struct {
	RunID, OwnerMode, ControllerThread, ControlToken          string
	OwnerContextID, OwnerBirth, OwnerGrantID, OwnerGrantToken string
	OwnerPID, PeerPID                                         int
	PeerBirth                                                 string
	NativeOwnerCapabilityPath, NativeHostGeneration           string
	NativeAttachmentProofSHA256                               string
}
type BridgeHookProof struct {
	BindingID, HookToken, PeerBirth string
	PeerPID                         int
}
type bridgeOwnerKey struct{}
type bridgeHookKey struct{}

func WithBridgeOwnerProof(ctx context.Context, proof BridgeOwnerProof) context.Context {
	return context.WithValue(ctx, bridgeOwnerKey{}, proof)
}
func BridgeOwnerProofFromContext(ctx context.Context) (BridgeOwnerProof, bool) {
	p, ok := ctx.Value(bridgeOwnerKey{}).(BridgeOwnerProof)
	return p, ok
}
func WithBridgeHookProof(ctx context.Context, proof BridgeHookProof) context.Context {
	return context.WithValue(ctx, bridgeHookKey{}, proof)
}

// BridgeCapabilityPath is pure: the launcher may calculate it before spawn.
// The path grants nothing without the later private file and DB token hash.
func BridgeCapabilityPath(stateDir, runID string) string {
	if !filepath.IsAbs(stateDir) || runID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(runID))
	return filepath.Join(stateDir, "bridge", hex.EncodeToString(sum[:])+".json")
}

func bridgeEqualHash(stored, secret string) bool {
	if stored == "" || secret == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(stored), []byte(runtimeHash(secret))) == 1
}

func bridgeWorkerTx(ctx context.Context, tx *sql.Tx, ancestry map[int]string) (bool, error) {
	rows, err := tx.QueryContext(ctx, workerAncestrySQL)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var pid int
		var birth string
		if err = rows.Scan(&pid, &birth); err != nil {
			return false, err
		}
		if ancestry[pid] == birth {
			return true, nil
		}
	}
	return false, rows.Err()
}

func bridgePeerAncestry(ctx context.Context, tx *sql.Tx, pid int, birth string) (map[int]string, error) {
	if pid <= 0 || birth == "" {
		return nil, CodeError("bridge_sender_unknown")
	}
	ancestry, err := process.AncestryContext(ctx, pid)
	if err != nil || ancestry[pid] != birth {
		return nil, CodeError("bridge_sender_unknown")
	}
	worker, err := bridgeWorkerTx(ctx, tx, ancestry)
	if err != nil {
		return nil, err
	}
	if worker {
		return nil, CodeError("worker_dispatch_forbidden")
	}
	return ancestry, nil
}

func bridgeOwnerTx(ctx context.Context, tx *sql.Tx, runID string) (BridgeOwnerProof, error) {
	p, ok := ctx.Value(bridgeOwnerKey{}).(BridgeOwnerProof)
	if !ok || p.RunID != runID || p.ControllerThread == "" || p.ControlToken == "" || p.OwnerContextID == "" {
		return p, CodeError("owner_mismatch")
	}
	if err := ctx.Err(); err != nil {
		return p, err
	}
	var controller, contextID, birth, controlHash string
	var pid int
	err := tx.QueryRowContext(ctx, `SELECT r.controller_thread,r.origin_context_id,r.origin_pid,r.origin_birth,c.token_hash FROM runs r JOIN run_control c ON c.run_id=r.id WHERE r.id=?`, runID).Scan(&controller, &contextID, &pid, &birth, &controlHash)
	if err != nil {
		return p, ErrNotFound
	}
	if controller != p.ControllerThread || contextID != p.OwnerContextID || pid != p.OwnerPID || birth != p.OwnerBirth || !bridgeEqualHash(controlHash, p.ControlToken) {
		return p, CodeError("owner_mismatch")
	}
	ancestry, err := bridgePeerAncestry(ctx, tx, p.PeerPID, p.PeerBirth)
	if err != nil {
		return p, err
	}
	if ancestry[pid] != birth {
		return p, CodeError("owner_peer_mismatch")
	}
	if p.OwnerMode == "managed" || p.OwnerMode == "local" {
		var grantHash, grantController, grantBirth string
		var grantPID int
		err = tx.QueryRowContext(ctx, `SELECT token_hash,controller_thread,origin_pid,origin_birth FROM owner_grants WHERE id=?`, p.OwnerGrantID).Scan(&grantHash, &grantController, &grantPID, &grantBirth)
		if err != nil || p.OwnerGrantID != contextID || grantController != controller || grantPID != pid || grantBirth != birth || !bridgeEqualHash(grantHash, p.OwnerGrantToken) {
			return p, CodeError("owner_capability_mismatch")
		}
	} else if p.OwnerMode == "native" {
		if p.NativeOwnerCapabilityPath == "" || p.NativeHostGeneration == "" || p.NativeAttachmentProofSHA256 == "" {
			return p, CodeError("owner_capability_invalid")
		}
		current, err := nativebridgecli.VerifyOwnerCapability(p.NativeOwnerCapabilityPath)
		if err != nil {
			return p, CodeError("owner_capability_invalid")
		}
		if err := ctx.Err(); err != nil {
			return p, err
		}
		if current.OwnerCapability != p.NativeOwnerCapabilityPath || current.ControllerThread != controller || current.OriginContextID != contextID || current.OriginPID != pid || current.OriginBirth != birth || current.HostGeneration != p.NativeHostGeneration || current.AttachmentProofSHA256 != p.NativeAttachmentProofSHA256 {
			return p, CodeError("owner_capability_mismatch")
		}
	} else {
		return p, CodeError("owner_mode_invalid")
	}
	return p, nil
}

func bridgeNativeIdentity(ctx context.Context, tx *sql.Tx, nativePID int, nativeBirth, digest string, ownerPID int, ownerBirth string) error {
	ancestry, err := bridgePeerAncestry(ctx, tx, nativePID, nativeBirth)
	if err != nil {
		return err
	}
	if ancestry[ownerPID] != ownerBirth || nativePID == ownerPID {
		return CodeError("owner_peer_mismatch")
	}
	return bridgeExecutableDigest(ctx, nativePID, digest)
}

func bridgeExecutableDigest(ctx context.Context, pid int, digest string) error {
	path, err := process.ExecutablePath(pid)
	if err != nil {
		return CodeError("bridge_sender_unknown")
	}
	actual, err := process.ExecutableDigest(ctx, path)
	if err != nil || actual != strings.ToLower(digest) {
		return CodeError("bridge_sender_unknown")
	}
	return nil
}

func osRemoveBridgeFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !storeOwned(info) {
		return CodeError("bridge_capability_invalid")
	}
	return os.Remove(path)
}

type bridgeBinding struct {
	view                contract.BridgeBindingView
	HookHash, RequestID string
}

const bridgeSelect = `SELECT binding_id,run_id,provider_id,native_session_id,owner_context_id,owner_pid,owner_birth,native_pid,native_birth,native_executable_sha256,owner_epoch,role,transport_mode,manifest_sha256,status,candidate_observation_event_id,candidate_observation_hash,capability_file,hook_token_hash,request_id FROM bridge_bindings`

func scanBridgeBinding(row *sql.Row) (bridgeBinding, error) {
	var b bridgeBinding
	b.view.SchemaVersion = contract.BridgeSchemaVersion
	err := row.Scan(&b.view.BindingID, &b.view.RunID, &b.view.ProviderID, &b.view.NativeSessionID, &b.view.OwnerContextID, &b.view.OwnerPID, &b.view.OwnerBirth, &b.view.NativePID, &b.view.NativeBirth, &b.view.NativeExecutableSHA256, &b.view.OwnerEpoch, &b.view.Role, &b.view.TransportMode, &b.view.ManifestSHA256, &b.view.Status, &b.view.CandidateObservationEventID, &b.view.CandidateObservationHash, &b.view.CapabilityFile, &b.HookHash, &b.RequestID)
	return b, err
}

// BridgeBindingIdentity is read-only preflight for coordinator peer checks.
// Every mutation repeats the binding and run checks inside its transaction.
func (d *DB) BridgeBindingIdentity(ctx context.Context, id string) (contract.BridgeBindingView, error) {
	b, err := scanBridgeBinding(d.sql.QueryRowContext(ctx, bridgeSelect+` WHERE binding_id=?`, id))
	if err != nil {
		return b.view, err
	}
	return b.view, nil
}

func bridgeSenderExited(ctx context.Context, pid int, birth string) bool {
	current, err := process.BirthContext(ctx, pid)
	if err == nil {
		return current != birth
	}
	if ctx.Err() != nil {
		return false
	}
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func (d *DB) BindBridge(ctx context.Context, req contract.BridgeBindRequest) (contract.BridgeBindingView, error) {
	var empty contract.BridgeBindingView
	if req.SchemaVersion != 1 || req.RequestID == "" || req.RunID == "" || req.ProviderID == "" || req.NativePID <= 0 || req.NativeBirth == "" || !validDigest(req.NativeExecutableSHA256) || req.TransportMode != "hook_pull" || !validDigest(req.ManifestSHA256) {
		return empty, CodeError("bridge_invalid_input")
	}
	if err := d.bridgeLockContext(ctx); err != nil {
		return empty, err
	}
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	owner, err := bridgeOwnerTx(ctx, tx, req.RunID)
	if err != nil {
		return empty, err
	}
	if req.OwnerMode != owner.OwnerMode {
		return empty, CodeError("owner_mode_invalid")
	}
	var mode, created, cutover string
	err = tx.QueryRowContext(ctx, `SELECT r.delivery_mode,r.created_at,m.bridge_cutover_at FROM runs r CROSS JOIN schema_meta m WHERE r.id=?`, req.RunID).Scan(&mode, &created, &cutover)
	if err != nil {
		return empty, err
	}
	createdAt, e1 := time.Parse(time.RFC3339Nano, created)
	cutoverAt, e2 := time.Parse(time.RFC3339Nano, cutover)
	if mode != "collect" || e1 != nil || e2 != nil || !createdAt.After(cutoverAt) {
		return empty, CodeError("bridge_mode_unsupported")
	}
	if err = bridgeNativeIdentity(ctx, tx, req.NativePID, req.NativeBirth, req.NativeExecutableSHA256, owner.OwnerPID, owner.OwnerBirth); err != nil {
		return empty, err
	}
	old, oldErr := scanBridgeBinding(tx.QueryRowContext(ctx, bridgeSelect+` WHERE run_id=? AND owner_context_id=? ORDER BY owner_epoch DESC LIMIT 1`, req.RunID, owner.OwnerContextID))
	if oldErr != nil && !errors.Is(oldErr, sql.ErrNoRows) {
		return empty, oldErr
	}
	epoch := uint64(1)
	if oldErr == nil {
		if old.RequestID == req.RequestID && old.view.NativePID == req.NativePID && old.view.NativeBirth == req.NativeBirth && old.view.NativeExecutableSHA256 == req.NativeExecutableSHA256 && old.view.ProviderID == req.ProviderID && old.view.ManifestSHA256 == req.ManifestSHA256 {
			if err = bridgeBindingPublicSafe(ctx, tx, old.view); err != nil {
				return empty, err
			}
			_ = tx.Rollback()
			return old.view, nil
		}
		var pending int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM bridge_delivery_attempts WHERE binding_id=? AND state IN ('CLAIMED','EMITTING','OFFERED','UNCERTAIN')`, old.view.BindingID).Scan(&pending); err != nil {
			return empty, err
		}
		if pending != 0 || !bridgeSenderExited(ctx, old.view.NativePID, old.view.NativeBirth) {
			return empty, ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `UPDATE bridge_bindings SET status='STALE',hook_token_hash='',updated_at=? WHERE binding_id=?`, time.Now().UTC().Format(time.RFC3339Nano), old.view.BindingID); err != nil {
			return empty, err
		}
		epoch = old.view.OwnerEpoch + 1
	}
	id, err := newID("binding")
	if err != nil {
		return empty, err
	}
	token, err := newID("hook")
	if err != nil {
		return empty, err
	}
	path := BridgeCapabilityPath(filepath.Dir(d.path), req.RunID)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	row := contract.BridgeBindingView{SchemaVersion: 1, BindingID: id, RunID: req.RunID, ProviderID: req.ProviderID, OwnerContextID: owner.OwnerContextID, OwnerPID: owner.OwnerPID, OwnerBirth: owner.OwnerBirth, NativePID: req.NativePID, NativeBirth: req.NativeBirth, NativeExecutableSHA256: strings.ToLower(req.NativeExecutableSHA256), OwnerEpoch: epoch, Role: "owner", TransportMode: "hook_pull", ManifestSHA256: strings.ToLower(req.ManifestSHA256), Status: "PENDING_SESSION", CapabilityFile: path}
	bytes := bridgeBindingBytes(row, runtimeHash(token), req.RequestID, now, now)
	if err = d.storageAdmission(ctx, tx, req.RunID, bytes, false); err != nil {
		return empty, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO bridge_bindings(binding_id,run_id,provider_id,owner_context_id,owner_pid,owner_birth,native_pid,native_birth,native_executable_sha256,owner_epoch,role,transport_mode,manifest_sha256,status,hook_token_hash,capability_file,request_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, req.RunID, req.ProviderID, owner.OwnerContextID, owner.OwnerPID, owner.OwnerBirth, req.NativePID, req.NativeBirth, row.NativeExecutableSHA256, epoch, "owner", "hook_pull", row.ManifestSHA256, row.Status, runtimeHash(token), path, req.RequestID, now, now)
	if err != nil {
		return empty, mapConflict(err)
	}
	if err = bridgeBindingPublicSafe(ctx, tx, row); err != nil {
		return empty, err
	}
	if err = writeBridgeCapability(path, id, epoch, token); err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return row, nil
}

// A binding view is public on both the first Bind and its idempotent replay.
// Check the authenticated request's raw secrets and the bounded built-in
// capabilities whose hashes are already available in this transaction.
func bridgeBindingPublicSafe(ctx context.Context, tx *sql.Tx, view contract.BridgeBindingView) error {
	if err := bridgePublicSafe(ctx, view); err != nil {
		return err
	}
	return bridgeStoredSecretSafe(ctx, tx, view.RunID, view.BindingID, view)
}

func bridgeBindingBytes(v contract.BridgeBindingView, hash, request, created, updated string) int64 {
	return int64(len(v.BindingID) + len(v.RunID) + len(v.ProviderID) + len(v.NativeSessionID) + len(v.OwnerContextID) + len(v.OwnerBirth) + len(v.NativeBirth) + len(v.NativeExecutableSHA256) + len(v.Role) + len(v.TransportMode) + len(v.ManifestSHA256) + len(v.Status) + len(v.CandidateObservationEventID) + len(v.CandidateObservationHash) + len(hash) + len(v.CapabilityFile) + len(request) + len(created) + len(updated) + 3*8)
}

func writeBridgeCapability(path, id string, epoch uint64, token string) error {
	dir := filepath.Dir(path)
	state := filepath.Dir(dir)
	canonical, err := filepath.EvalSymlinks(state)
	if err != nil || canonical != state {
		return CodeError("bridge_capability_invalid")
	}
	stateInfo, err := os.Lstat(state)
	if err != nil || !stateInfo.IsDir() || stateInfo.Mode().Perm() != 0700 || !storeOwned(stateInfo) {
		return CodeError("bridge_capability_invalid")
	}
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !storeOwned(info) {
		return CodeError("bridge_capability_invalid")
	}
	if old, err := os.Lstat(path); err == nil {
		if !old.Mode().IsRegular() || old.Mode().Perm() != 0600 || !storeOwned(old) {
			return CodeError("bridge_capability_invalid")
		}
		stat, ok := old.Sys().(*syscall.Stat_t)
		if !ok || stat.Nlink != 1 || old.Size() > 8192 {
			return CodeError("bridge_capability_invalid")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := json.Marshal(struct {
		SchemaVersion  int    `json:"schema_version"`
		BindingID      string `json:"binding_id"`
		OwnerEpoch     uint64 `json:"owner_epoch"`
		HookCapability string `json:"hook_capability"`
	}{1, id, epoch, token})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "."+id+".tmp"), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(append(data, '\n')); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	_ = d.Close()
	return err
}

// BridgeHookCapability is private local input for B04's Hook adapter. The
// reader checks the descriptor and path identity to reject symlinks and swaps.
type BridgeHookCapability struct {
	SchemaVersion  int    `json:"schema_version"`
	BindingID      string `json:"binding_id"`
	OwnerEpoch     uint64 `json:"owner_epoch"`
	HookCapability string `json:"hook_capability"`
}

func ReadBridgeCapability(path string) (BridgeHookCapability, error) {
	var cap BridgeHookCapability
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(filepath.Base(path)) != 69 || !strings.HasSuffix(path, ".json") {
		return cap, CodeError("bridge_capability_invalid")
	}
	name := strings.TrimSuffix(filepath.Base(path), ".json")
	if len(name) != 64 {
		return cap, CodeError("bridge_capability_invalid")
	}
	if _, err := hex.DecodeString(name); err != nil {
		return cap, CodeError("bridge_capability_invalid")
	}
	dir := filepath.Dir(path)
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil || canonical != dir {
		return cap, CodeError("bridge_capability_invalid")
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode().Perm() != 0700 || !storeOwned(dirInfo) {
		return cap, CodeError("bridge_capability_invalid")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return cap, CodeError("bridge_capability_invalid")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !storeOwned(info) || info.Size() <= 0 || info.Size() > 8192 {
		return cap, CodeError("bridge_capability_invalid")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return cap, CodeError("bridge_capability_invalid")
	}
	raw := make([]byte, info.Size())
	if _, err = io.ReadFull(io.NewSectionReader(f, 0, info.Size()), raw); err != nil {
		return cap, CodeError("bridge_capability_invalid")
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) || after.Mode().Perm() != 0600 {
		return cap, CodeError("bridge_capability_invalid")
	}
	if err = contract.DecodeBridgeStrict(raw, &cap); err != nil || cap.SchemaVersion != 1 || cap.BindingID == "" || cap.OwnerEpoch == 0 || cap.HookCapability == "" {
		return BridgeHookCapability{}, CodeError("bridge_capability_invalid")
	}
	return cap, nil
}
