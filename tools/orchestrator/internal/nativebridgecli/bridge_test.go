package nativebridgecli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
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
	root := t.TempDir()
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	capabilityPath := filepath.Join(root, "generation-00000002.json")
	capability := map[string]any{
		"version": 1, "path": capabilityPath,
		"controller_thread_id": "01a097ff-f802-7b02-9159-4b2bd0552626",
		"origin_context_id":    "origin_0123456789abcdef0123456789abcdef",
		"origin_pid":           os.Getpid(), "origin_birth": birth,
		"host_generation": "generation-00000002", "generation_number": 2,
		"attachment_proof_sha256": strings.Repeat("a", 64),
		"lease_id":                "012345abcdef", "activation_id": strings.Repeat("b", 32),
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
		"attachment_proof_sha256": strings.Repeat("a", 64),
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
}
