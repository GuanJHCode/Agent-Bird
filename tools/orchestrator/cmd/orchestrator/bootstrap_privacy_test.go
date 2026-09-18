package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestBootstrapV2DiskHasNoTokenFields(t *testing.T) {
	state := t.TempDir()
	b := hostBootstrap{SocketPath: filepath.Join(state, "coordinator.sock"), SpoolRoot: filepath.Join(state, "spool"), ProducerID: "host-launch-fixture", Hello: contract.HostHello{LaunchID: "host-launch-fixture", LaunchToken: "secret-launch-sentinel", OriginContextID: "origin", OriginPID: os.Getpid(), OriginBirth: "birth", HostGeneration: "generation", Executable: "/fixture/orchestrator"}}
	path, err := writeBootstrap(state, b)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("token")) || bytes.Contains(data, []byte(b.Hello.LaunchToken)) {
		t.Fatal("bootstrap exposes a token field or value")
	}
	var m map[string]any
	if err = json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m["version"] != float64(2) {
		t.Fatalf("metadata version=%v", m["version"])
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("metadata privacy: %v", err)
	}
}

func TestBootstrapFDRejectsInvalidInputAndClosesDescriptor(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"truncated", `{"version":2`},
		{"oversized", strings.Repeat(" ", 65537)},
		{"unknown", `{"version":2,"secret_field":"secret-sentinel"}`},
		{"trailing", `{} {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			fd, err := syscall.Dup(int(reader.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			_ = reader.Close()
			done := make(chan struct{})
			go func() { defer close(done); _, _ = writer.Write([]byte(tc.body)); _ = writer.Close() }()
			err = sourceHost(context.Background(), []string{"--state-dir", t.TempDir(), "--bootstrap-fd", strconv.Itoa(fd)})
			if errorCode(err) != "invalid_host_bootstrap" {
				t.Fatalf("invalid FD payload: %v", err)
			}
			var stat syscall.Stat_t
			if err = syscall.Fstat(fd, &stat); err != syscall.EBADF {
				t.Fatalf("bootstrap FD remains open: %v", err)
			}
			<-done
		})
	}
	for _, fd := range []string{"-1", "0", "1", "2", "999999"} {
		if err := sourceHost(context.Background(), []string{"--state-dir", t.TempDir(), "--bootstrap-fd", fd}); errorCode(err) != "invalid_bootstrap_fd" {
			t.Fatalf("fd=%s: %v", fd, err)
		}
	}
}

func TestBootstrapFDRequiresEOFAndPipe(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	fd, err := syscall.Dup(int(reader.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	_, _ = writer.Write([]byte(`{"version":2}`))
	start := time.Now()
	_, err = readBootstrapFD(fd)
	if errorCode(err) != "invalid_host_bootstrap" {
		t.Fatalf("open-ended pipe: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 4*time.Second || elapsed > 7*time.Second {
		t.Fatalf("read deadline elapsed=%s", elapsed)
	}
	var stat syscall.Stat_t
	if err = syscall.Fstat(fd, &stat); err != syscall.EBADF {
		t.Fatalf("timed out FD remains open: %v", err)
	}
	file, err := os.CreateTemp(t.TempDir(), "regular")
	if err != nil {
		t.Fatal(err)
	}
	fd, err = syscall.Dup(int(file.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err = readBootstrapFD(fd); errorCode(err) != "invalid_bootstrap_fd" {
		t.Fatalf("regular file accepted: %v", err)
	}
	if err = syscall.Fstat(fd, &stat); err != syscall.EBADF {
		t.Fatalf("regular FD remains open: %v", err)
	}
}

func TestBootstrapControlReconstructionAndLegacyReadOnly(t *testing.T) {
	state := t.TempDir()
	b := hostBootstrap{Version: 2, SocketPath: filepath.Join(state, "coordinator.sock"), SpoolRoot: filepath.Join(state, "spool"), ProducerID: "launch", Hello: contract.HostHello{LaunchID: "launch", LaunchToken: "launch-secret-sentinel", OriginContextID: "origin", OriginPID: os.Getpid(), OriginBirth: "birth", HostGeneration: "generation", Executable: "/fixture/orchestrator"}}
	path, err := writeBootstrap(state, b)
	if err != nil {
		t.Fatal(err)
	}
	capPath, err := writeControlCapability(state, "run", "controller", "control-secret-sentinel", path, b.Hello.LaunchID, b.Hello.LaunchToken)
	if err != nil {
		t.Fatal(err)
	}
	cap, err := loadControlCapability(capPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeCap, err := os.ReadFile(capPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := loadHostBootstrap(cap)
	if err != nil || loaded != b {
		t.Fatalf("reconstruction differs: err=%v", err)
	}
	loaded.Hello.OriginPID = 99
	loaded.Hello.OriginBirth = "new-birth"
	loaded.Hello.HostGeneration = "new-generation"
	if err = replacePrivateJSON(path, bootstrapMetadata(loaded)); err != nil {
		t.Fatal(err)
	}
	afterCap, _ := os.ReadFile(capPath)
	if !bytes.Equal(beforeCap, afterCap) {
		t.Fatal("metadata rebind changed immutable control")
	}
	roundTrip, err := loadHostBootstrap(cap)
	if err != nil || roundTrip != loaded {
		t.Fatalf("metadata origin update did not roundtrip: %v", err)
	}
	for _, mutation := range []string{"missing-token", "wrong-id", "token-in-metadata", "v1-in-metadata"} {
		t.Run(mutation, func(t *testing.T) {
			bad := cap
			switch mutation {
			case "missing-token":
				bad.HostLaunchToken = ""
			case "wrong-id":
				bad.HostLaunchID = "other"
			case "token-in-metadata":
				data, _ := json.Marshal(bootstrapMetadata(loaded))
				data = bytes.Replace(data, []byte(`"launch_id":"launch"`), []byte(`"launch_id":"launch","launch_token":"secret-injection"`), 1)
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "v1-in-metadata":
				legacy := b
				legacy.Version = 1
				if err := replacePrivateJSON(path, legacy); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := loadHostBootstrap(bad); err == nil {
				t.Fatal("invalid metadata/control accepted")
			}
			if err := replacePrivateJSON(path, bootstrapMetadata(loaded)); err != nil {
				t.Fatal(err)
			}
		})
	}
	// Legacy material remains byte-for-byte v1 and is never migrated or deleted.
	legacy := b
	legacy.Version = 1
	legacyPath := filepath.Join(state, "legacy.json")
	if err = writeExclusiveJSON(legacyPath, legacy); err != nil {
		t.Fatal(err)
	}
	legacyCap := controlCapability{Version: 1, RunID: "run", ControllerThread: "controller", ControlToken: "control-secret-sentinel", BootstrapPath: legacyPath}
	before, _ := os.ReadFile(legacyPath)
	read, err := loadHostBootstrap(legacyCap)
	if err != nil || read != legacy {
		t.Fatalf("legacy read: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = sourceHost(ctx, []string{"--state-dir", state, "--bootstrap", legacyPath})
	after, err := os.ReadFile(legacyPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("legacy file altered/removed: %v", err)
	}
	// v2 metadata cannot start by falling back to the legacy path reader.
	if err = sourceHost(ctx, []string{"--state-dir", state, "--bootstrap", path}); errorCode(err) != "invalid_host_bootstrap" {
		t.Fatalf("v2 disk fallback: %v", err)
	}
}

func TestBootstrapSourceLaunchPipeExcludesSecretsFromArgumentsEnvironmentAndLog(t *testing.T) {
	state := t.TempDir()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(state, "capture-source")
	// This helper is only the subprocess transport test. It reads and closes the
	// pipe, then persists booleans and public argv; it never writes the secret.
	body := "#!" + python + "\nimport os,json,sys\nwith os.fdopen(3) as f: b=json.load(f)\nt=b['hello']['launch_token']\nassert t not in '\\0'.join(sys.argv)\nassert t not in '\\0'.join(os.environ.values())\nassert sys.argv[-2:]==['--bootstrap-fd','3']\ntry: os.fstat(3); closed=False\nexcept OSError: closed=True\nwith open(" + strconv.Quote(filepath.Join(state, "observed.json")) + ",'w') as f: json.dump({'pipe_closed':closed,'argv':sys.argv[1:]},f)\n"
	if err = os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	b := hostBootstrap{Version: 2, SocketPath: filepath.Join(state, "coordinator.sock"), SpoolRoot: filepath.Join(state, "spool"), ProducerID: "launch", Hello: contract.HostHello{LaunchID: "launch", LaunchToken: "transport-secret-sentinel"}}
	if err = startSourceHost(script, state, b); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var observed []byte
	for time.Now().Before(deadline) {
		observed, err = os.ReadFile(filepath.Join(state, "observed.json"))
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var result struct {
		Closed bool     `json:"pipe_closed"`
		Args   []string `json:"argv"`
	}
	if err != nil || json.Unmarshal(observed, &result) != nil || !result.Closed {
		t.Fatalf("transport observer incomplete: %v", err)
	}
	log, err := os.ReadFile(filepath.Join(state, "source-host.log"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(log, []byte(b.Hello.LaunchToken)) || len(log) != 0 {
		t.Fatalf("transport helper wrote unexpected log (%d bytes)", len(log))
	}
}

func TestBootstrapLegacyRebindRefusedBeforeStateMutation(t *testing.T) {
	root := t.TempDir()
	b := hostBootstrap{Version: 1, Hello: contract.HostHello{LaunchID: "legacy", LaunchToken: "legacy-secret", OriginContextID: "origin"}}
	path := filepath.Join(root, "legacy-bootstrap.json")
	if err := writeExclusiveJSON(path, b); err != nil {
		t.Fatal(err)
	}
	c := controlCapability{Version: 1, RunID: "run", ControllerThread: "controller", ControlToken: "control-secret", BootstrapPath: path}
	capPath := filepath.Join(root, "control.json")
	if err := writeExclusiveJSON(capPath, c); err != nil {
		t.Fatal(err)
	}
	ownerPath := filepath.Join(root, "owner.json")
	if err := writeExclusiveJSON(ownerPath, store.OwnerGrant{Version: 1, Kind: "local-owner", ControllerThread: "controller", OriginPID: os.Getpid(), OriginBirth: "new-birth", HostGeneration: "new-generation"}); err != nil {
		t.Fatal(err)
	}
	request := filepath.Join(root, "request.json")
	if err := writeExclusiveJSON(request, map[string]any{"version": 1, "owner_mode": "local", "owner_capability": ownerPath, "control_file": capPath}); err != nil {
		t.Fatal(err)
	}
	beforeBootstrap, _ := os.ReadFile(path)
	beforeControl, _ := os.ReadFile(capPath)
	state := filepath.Join(root, "uncreated-state")
	err := rebindLocalOwner(context.Background(), state, request, io.Discard)
	if errorCode(err) != "legacy_bootstrap_rebind_unsupported" {
		t.Fatalf("legacy rebind: %v", err)
	}
	if _, err = os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("legacy rebind created coordinator state")
	}
	afterBootstrap, _ := os.ReadFile(path)
	afterControl, _ := os.ReadFile(capPath)
	if !bytes.Equal(beforeBootstrap, afterBootstrap) || !bytes.Equal(beforeControl, afterControl) {
		t.Fatal("legacy rebind modified recovery material")
	}
}

func TestBootstrapV2CancellationKeepsMetadataAndControl(t *testing.T) {
	state := t.TempDir()
	b := hostBootstrap{Version: 2, SocketPath: filepath.Join(state, "coordinator.sock"), SpoolRoot: filepath.Join(state, "spool"), ProducerID: "launch", Hello: contract.HostHello{LaunchID: "launch", LaunchToken: "secret-sentinel"}}
	metadata, err := writeBootstrap(state, b)
	if err != nil {
		t.Fatal(err)
	}
	control, err := writeControlCapability(state, "run", "thread", "control-sentinel", metadata, "launch", b.Hello.LaunchToken)
	if err != nil {
		t.Fatal(err)
	}
	beforeMetadata, _ := os.ReadFile(metadata)
	beforeControl, _ := os.ReadFile(control)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Dup(int(reader.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	body, _ := json.Marshal(b)
	_, _ = writer.Write(body)
	_ = writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = sourceHost(ctx, []string{"--state-dir", state, "--bootstrap-fd", strconv.Itoa(fd)})
	afterMetadata, err := os.ReadFile(metadata)
	if err != nil {
		t.Fatal(err)
	}
	afterControl, err := os.ReadFile(control)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeMetadata, afterMetadata) || !bytes.Equal(beforeControl, afterControl) {
		t.Fatal("cancelled source altered recovery material")
	}
	var stat syscall.Stat_t
	if err = syscall.Fstat(fd, &stat); err != syscall.EBADF {
		t.Fatalf("successful-read FD remains open: %v", err)
	}
}
