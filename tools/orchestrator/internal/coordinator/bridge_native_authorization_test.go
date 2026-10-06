package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/nativebridgecli"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestBridgeNativeAttachmentRecheckedAfterAuthorization(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "b02-native-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	s, err := NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	ownerBirth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	service := exec.Command("/bin/sleep", "30")
	backend := exec.Command("/bin/sleep", "30")
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Process.Kill(); _, _ = service.Process.Wait() }()
	if err := backend.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backend.Process.Kill(); _, _ = backend.Process.Wait() }()
	serviceBirth, err := process.Birth(service.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	backendBirth, err := process.Birth(backend.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	ownerExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ownerExe, err = filepath.EvalSymlinks(ownerExe)
	if err != nil {
		t.Fatal(err)
	}
	sleepExe, err := filepath.EvalSymlinks("/bin/sleep")
	if err != nil {
		t.Fatal(err)
	}
	ownerDigest, err := process.ExecutableDigest(ctx, ownerExe)
	if err != nil {
		t.Fatal(err)
	}
	sleepDigest, err := process.ExecutableDigest(ctx, sleepExe)
	if err != nil {
		t.Fatal(err)
	}
	privateSocket := filepath.Join(root, "native-private.sock")
	listener, err := net.Listen("unix", privateSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(privateSocket, 0600); err != nil {
		t.Fatal(err)
	}
	socketInfo, err := os.Lstat(privateSocket)
	if err != nil {
		t.Fatal(err)
	}
	stat := socketInfo.Sys().(*syscall.Stat_t)
	thread := "01a097ff-f802-7b02-9159-4b2bd0552626"
	contextID := "origin_0123456789abcdef0123456789abcdef"
	attachment := map[string]any{
		"version": 1, "profile_id": "profile-a", "controller_thread_id": thread,
		"controller_epoch": 1, "owner_context_sha256": strings.Repeat("c", 64),
		"lease_id": "012345abcdef", "owner_connection_id": "conn-1-012345abcdef",
		"activation_id": strings.Repeat("b", 32), "manifest_sha256": strings.Repeat("d", 64),
		"helper_grant_sha256": strings.Repeat("e", 64),
		"service_identity": map[string]any{"pid": service.Process.Pid, "uid": os.Getuid(), "birth": serviceBirth,
			"executable": sleepExe, "executable_sha256": sleepDigest},
		"backend_identity": map[string]any{"pid": backend.Process.Pid, "uid": os.Getuid(), "birth": backendBirth,
			"executable": sleepExe, "executable_sha256": sleepDigest},
		"private_socket":          privateSocket,
		"private_socket_identity": []uint64{uint64(stat.Dev), uint64(stat.Ino), uint64(stat.Uid), uint64(stat.Mode)},
		"origin_process": map[string]any{"pid": os.Getpid(), "uid": os.Getuid(), "birth": ownerBirth,
			"executable": ownerExe, "executable_sha256": ownerDigest},
	}
	attachmentJSON, err := json.Marshal(attachment)
	if err != nil {
		t.Fatal(err)
	}
	proofHash := sha256.Sum256(attachmentJSON)
	ownerFile := filepath.Join(root, "native-owner.json")
	capability := map[string]any{
		"version": 1, "path": ownerFile, "controller_thread_id": thread,
		"origin_context_id": contextID, "origin_pid": os.Getpid(), "origin_birth": ownerBirth,
		"host_generation": "generation-00000002", "generation_number": 2,
		"attachment_proof_sha256": hex.EncodeToString(proofHash[:]),
		"lease_id":                "012345abcdef", "activation_id": strings.Repeat("b", 32),
		"owner_attachment": attachment,
	}
	ownerBytes, err := json.Marshal(capability)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ownerFile, ownerBytes, 0600); err != nil {
		t.Fatal(err)
	}
	controlDir := filepath.Join(state, "control")
	if err := os.Mkdir(controlDir, 0700); err != nil {
		t.Fatal(err)
	}
	controlFile := filepath.Join(controlDir, "run.json")
	controlBytes, _ := json.Marshal(map[string]any{"version": 2, "run_id": "run", "controller_thread": thread, "control_token": "control", "bootstrap_path": filepath.Join(state, "host-bootstrap", "x.json"), "host_launch_id": "launch", "host_launch_token": "token"})
	if err := os.WriteFile(controlFile, controlBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.SQL().Exec(`INSERT INTO runs(id,controller_thread,plan_revision,origin_context_id,origin_pid,origin_birth,status,created_at,delivery_mode) VALUES(?,?,?,?,?,?,'queued',?,'collect')`, "run", thread, 1, contextID, os.Getpid(), ownerBirth, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.SQL().Exec(`INSERT INTO run_control(run_id,token_hash) VALUES('run',?)`, bridgeTestHash("control")); err != nil {
		t.Fatal(err)
	}
	conn := bridgeTestUnixPeer(t, filepath.Join(root, "peer.sock"))
	defer conn.Close()
	bind := contract.BridgeBindRequest{SchemaVersion: 1, RequestID: "native-bind", RunID: "run", ProviderID: "claude", NativePID: backend.Process.Pid, NativeBirth: backendBirth, NativeExecutableSHA256: sleepDigest, TransportMode: "hook_pull", OwnerMode: "native", OwnerCapabilityFile: ownerFile, ControlFile: controlFile, ManifestSHA256: strings.Repeat("a", 64)}
	bindRaw, _ := json.Marshal(bind)
	authorized, decoded, err := s.AuthorizeBridgeBind(ctx, conn, bindRaw)
	if err != nil {
		t.Fatalf("real native attachment authorize: %v", err)
	}
	view, err := s.db.BindBridge(authorized, decoded)
	if err != nil || view.Status != "PENDING_SESSION" {
		t.Fatalf("real native attachment bind=%+v err=%v", view, err)
	}
	unbind := contract.BridgeUnbindRequest{BridgeOwnerRequest: contract.BridgeOwnerRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: view.BindingID, OwnerEpoch: 1, RequestID: "native-unbind"}, OwnerMode: "native", OwnerCapabilityFile: ownerFile, ControlFile: controlFile}}
	unbindRaw, _ := json.Marshal(unbind)
	authorized, decodedUnbind, err := s.AuthorizeBridgeUnbind(ctx, conn, unbindRaw)
	if err != nil {
		t.Fatal(err)
	}
	var beforeCount, beforeBytes int64
	if err := s.db.SQL().QueryRow(`SELECT COUNT(*) FROM bridge_bindings WHERE binding_id=?`, view.BindingID).Scan(&beforeCount); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SQL().QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'`).Scan(&beforeBytes); err != nil {
		t.Fatal(err)
	}
	assertDeniedWithoutWrite := func(label string) {
		t.Helper()
		if err := s.db.UnbindBridge(authorized, decodedUnbind); err == nil {
			t.Fatalf("%s retained Store mutation authority", label)
		}
		var count, afterBytes int64
		var status, tokenHash string
		if err := s.db.SQL().QueryRow(`SELECT COUNT(*),status,hook_token_hash FROM bridge_bindings WHERE binding_id=?`, view.BindingID).Scan(&count, &status, &tokenHash); err != nil || count != beforeCount || status != "PENDING_SESSION" || tokenHash == "" {
			t.Fatalf("%s half-wrote binding: count=%d status=%s hash=%q err=%v", label, count, status, tokenHash, err)
		}
		if err := s.db.SQL().QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'`).Scan(&afterBytes); err != nil || afterBytes != beforeBytes {
			t.Fatalf("%s changed usage: before=%d after=%d err=%v", label, beforeBytes, afterBytes, err)
		}
	}
	// Replacing the capability with a separately valid generation must not
	// inherit the generation authorized for this request.
	changedGeneration := make(map[string]any, len(capability))
	for key, value := range capability {
		changedGeneration[key] = value
	}
	changedGeneration["host_generation"] = "generation-00000003"
	changedGeneration["generation_number"] = 3
	generationBytes, _ := json.Marshal(changedGeneration)
	if err := os.WriteFile(ownerFile, generationBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativebridgecli.VerifyOwnerCapability(ownerFile); err != nil {
		t.Fatalf("changed generation fixture invalid: %v", err)
	}
	assertDeniedWithoutWrite("replaced host generation")
	if err := os.WriteFile(ownerFile, ownerBytes, 0600); err != nil {
		t.Fatal(err)
	}
	authorized, decodedUnbind, err = s.AuthorizeBridgeUnbind(ctx, conn, unbindRaw)
	if err != nil {
		t.Fatal(err)
	}
	changedAttachment := make(map[string]any, len(attachment))
	for key, value := range attachment {
		changedAttachment[key] = value
	}
	changedAttachment["owner_context_sha256"] = strings.Repeat("f", 64)
	changedAttachmentJSON, _ := json.Marshal(changedAttachment)
	changedProof := sha256.Sum256(changedAttachmentJSON)
	changedHash := make(map[string]any, len(capability))
	for key, value := range capability {
		changedHash[key] = value
	}
	changedHash["owner_attachment"] = changedAttachment
	changedHash["attachment_proof_sha256"] = hex.EncodeToString(changedProof[:])
	hashBytes, _ := json.Marshal(changedHash)
	if err := os.WriteFile(ownerFile, hashBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativebridgecli.VerifyOwnerCapability(ownerFile); err != nil {
		t.Fatalf("changed attachment hash fixture invalid: %v", err)
	}
	assertDeniedWithoutWrite("replaced attachment proof")
	if err := os.WriteFile(ownerFile, ownerBytes, 0600); err != nil {
		t.Fatal(err)
	}
	authorized, decodedUnbind, err = s.AuthorizeBridgeUnbind(ctx, conn, unbindRaw)
	if err != nil {
		t.Fatal(err)
	}
	savedOwnerFile := ownerFile + ".saved"
	if err := os.Rename(ownerFile, savedOwnerFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(savedOwnerFile, ownerFile); err != nil {
		t.Fatal(err)
	}
	assertDeniedWithoutWrite("replaced capability path")
	if err := os.Remove(ownerFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(savedOwnerFile, ownerFile); err != nil {
		t.Fatal(err)
	}
	authorized, decodedUnbind, err = s.AuthorizeBridgeUnbind(ctx, conn, unbindRaw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(privateSocket); err != nil {
		t.Fatal(err)
	}
	assertDeniedWithoutWrite("revoked native private socket")
}
