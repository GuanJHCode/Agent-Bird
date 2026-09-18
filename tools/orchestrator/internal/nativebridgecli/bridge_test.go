package nativebridgecli

import (
	"bytes"
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

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func fileSHA(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func privateJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunHelperUsesPinnedInterpreterAndInstalledBridgeScript(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	interpreter := filepath.Join(root, "pinned-python")
	if err := os.WriteFile(interpreter, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\ncat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	packageRoot := filepath.Join(root, "package")
	if err := os.MkdirAll(filepath.Join(packageRoot, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(packageRoot, "scripts", "native-product-bridge.py")
	if err := os.WriteFile(script, []byte("# pinned bridge\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(packageRoot, "manifest.json")
	privateJSON(t, manifestPath, map[string]any{"schema_version": 1, "files": []map[string]any{{"path": "scripts/native-product-bridge.py", "sha256": fileSHA(t, script), "mode": 0o700}}})
	requestPath := filepath.Join(root, "request.json")
	privateJSON(t, requestPath, LaunchRequest{Version: 1, Interpreter: interpreter, InterpreterSHA256: fileSHA(t, interpreter), PackageRoot: packageRoot, PackageManifest: manifestPath, PackageManifestSHA256: fileSHA(t, manifestPath)})
	var stdout, stderr bytes.Buffer
	err = RunHelper(context.Background(), requestPath, strings.NewReader("owner-config\n"), &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	want := "-B\n" + script + "\nhelper\nowner-config\n"
	if stdout.String() != want || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	if err = os.WriteFile(interpreter, []byte("#!/bin/sh\nexit 9\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	err = RunHelper(context.Background(), requestPath, strings.NewReader(""), &stdout, &stderr)
	if err == nil || err.Error() != "interpreter_changed" {
		t.Fatalf("err=%v", err)
	}
}

func TestVerifyRebindProjectsLiveOwnerWithoutReadingControlToken(t *testing.T) {
	root, err := os.MkdirTemp("/private/tmp", "nativebridge-owner-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	service := exec.Command("/bin/sleep", "30")
	backend := exec.Command("/bin/sleep", "30")
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	if err := backend.Start(); err != nil {
		_ = service.Process.Kill()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if service.ProcessState == nil {
			_ = service.Process.Kill()
			_ = service.Wait()
		}
		if backend.ProcessState == nil {
			_ = backend.Process.Kill()
			_ = backend.Wait()
		}
	})
	serviceBirth, err := process.Birth(service.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	backendBirth, err := process.Birth(backend.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	ownerExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ownerExecutable, err = filepath.EvalSymlinks(ownerExecutable)
	if err != nil {
		t.Fatal(err)
	}
	sleepExecutable, err := filepath.EvalSymlinks("/bin/sleep")
	if err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(root, "owner.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err = os.Chmod(socketPath, 0o600); err != nil {
		t.Fatal(err)
	}
	socketInfo, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	socketStat := socketInfo.Sys().(*syscall.Stat_t)
	attachment := map[string]any{
		"version": 1, "profile_id": "profile-a",
		"controller_thread_id": "01a097ff-f802-7b02-9159-4b2bd0552626",
		"controller_epoch":     1, "owner_context_sha256": strings.Repeat("c", 64),
		"lease_id": "012345abcdef", "owner_connection_id": "conn-1-012345abcdef",
		"activation_id": strings.Repeat("b", 32), "manifest_sha256": strings.Repeat("d", 64),
		"helper_grant_sha256": strings.Repeat("e", 64),
		"service_identity": map[string]any{"pid": service.Process.Pid, "uid": os.Getuid(), "birth": serviceBirth,
			"executable": sleepExecutable, "executable_sha256": fileSHA(t, sleepExecutable)},
		"backend_identity": map[string]any{"pid": backend.Process.Pid, "uid": os.Getuid(), "birth": backendBirth,
			"executable": sleepExecutable, "executable_sha256": fileSHA(t, sleepExecutable)},
		"private_socket":          socketPath,
		"private_socket_identity": []uint64{uint64(socketStat.Dev), uint64(socketStat.Ino), uint64(socketStat.Uid), uint64(socketStat.Mode)},
		"origin_process": map[string]any{"pid": os.Getpid(), "uid": os.Getuid(), "birth": birth,
			"executable": ownerExecutable, "executable_sha256": fileSHA(t, ownerExecutable)},
	}
	attachmentJSON, _ := json.Marshal(attachment)
	attachmentProof := sha256.Sum256(attachmentJSON)
	capabilityPath := filepath.Join(root, "generation-00000002.json")
	capability := map[string]any{
		"version": 1, "path": capabilityPath,
		"controller_thread_id": "01a097ff-f802-7b02-9159-4b2bd0552626",
		"origin_context_id":    "origin_0123456789abcdef0123456789abcdef",
		"origin_pid":           os.Getpid(), "origin_birth": birth,
		"host_generation": "generation-00000002", "generation_number": 2,
		"attachment_proof_sha256": hex.EncodeToString(attachmentProof[:]),
		"lease_id":                "012345abcdef", "activation_id": strings.Repeat("b", 32),
		"owner_attachment": attachment,
	}
	privateJSON(t, capabilityPath, capability)
	owner, err := VerifyOwnerCapability(capabilityPath)
	if err != nil {
		t.Fatal(err)
	}
	if owner.ControllerThread != capability["controller_thread_id"] || owner.OriginContextID != capability["origin_context_id"] || owner.OriginPID != os.Getpid() || owner.HostGeneration != "generation-00000002" {
		t.Fatalf("owner=%#v", owner)
	}
	controlPath := filepath.Join(root, "control.json")
	if err = os.WriteFile(controlPath, []byte("opaque-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(root, "rebind.json")
	privateJSON(t, requestPath, map[string]any{
		"version": 1, "owner_capability": capabilityPath, "control_file": controlPath,
		"controller_thread": capability["controller_thread_id"],
		"origin_context_id": capability["origin_context_id"], "origin_pid": os.Getpid(),
		"origin_birth": birth, "host_generation": "generation-00000002",
		"attachment_proof_sha256": hex.EncodeToString(attachmentProof[:]),
	})
	verified, err := VerifyRebind(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	if verified.OriginPID != os.Getpid() || verified.OriginBirth != birth || verified.ControlFile != controlPath || verified.HostGeneration != "generation-00000002" {
		t.Fatalf("verified=%#v", verified)
	}
	data, err := os.ReadFile(controlPath)
	if err != nil || string(data) != "opaque-token" {
		t.Fatalf("control changed: %q err=%v", data, err)
	}

	request := map[string]any{}
	raw, _ := os.ReadFile(requestPath)
	_ = json.Unmarshal(raw, &request)
	request["origin_birth"] = "stale"
	privateJSON(t, filepath.Join(root, "stale.json"), request)
	_, err = VerifyRebind(filepath.Join(root, "stale.json"))
	if err == nil || err.Error() != "owner_capability_mismatch" {
		t.Fatalf("err=%v", err)
	}
	if err = backend.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = backend.Wait()
	_, err = VerifyOwnerCapability(capabilityPath)
	if err == nil || err.Error() != "owner_attachment_not_live" {
		t.Fatalf("stale backend err=%v", err)
	}
}
