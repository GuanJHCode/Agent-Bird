package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// bootstrapObserverScript renders the transport observer helper used by the
// source-launch privacy tests. Every mode first publishes an atomic ready
// marker (mode and pid only, never the token) before reading FD 3. It then
// reads the bootstrap record and checks the launch token
// never reached argv or the environment, and reports whether the descriptor
// was actually closed. The modes differ only in how (or whether) the
// observation is published, so a half-written or missing file can never be
// read as "the pipe was closed".
func bootstrapObserverScript(t *testing.T, python, observedPath, readyPath, mode string) string {
	t.Helper()
	keepOpen := "False"
	if mode == "leak-fd" {
		keepOpen = "True"
	}
	body := "#!" + python + "\nimport os,json,sys,time\n" +
		"ready = " + strconv.Quote(readyPath) + "\n" +
		"handle = open(ready + '.partial', 'w')\n" +
		"json.dump({'mode': " + strconv.Quote(mode) + ", 'pid': os.getpid()}, handle)\n" +
		"handle.flush()\nos.fsync(handle.fileno())\nhandle.close()\nos.replace(ready + '.partial', ready)\n" +
		"keep_open = " + keepOpen + "\n" +
		"reader = os.fdopen(3)\n" +
		"bootstrap = json.load(reader)\n" +
		"if not keep_open: reader.close()\n" +
		"token = bootstrap['hello']['launch_token']\n" +
		"assert token not in '\\0'.join(sys.argv)\n" +
		"assert token not in '\\0'.join(os.environ.values())\n" +
		"assert sys.argv[-2:] == ['--bootstrap-fd', '3']\n" +
		"try:\n    os.fstat(3)\n    closed = False\nexcept OSError:\n    closed = True\n" +
		"path = " + strconv.Quote(observedPath) + "\n" +
		"def publish(record):\n    handle = open(path + '.partial', 'w')\n    json.dump(record, handle)\n    handle.flush()\n    os.fsync(handle.fileno())\n    handle.close()\n    os.replace(path + '.partial', path)\n"
	record := "publish({'pipe_closed': closed, 'argv': sys.argv[1:]})\n"
	switch mode {
	case "atomic":
		return body + record
	case "delayed":
		return body + "time.sleep(0.3)\n" + record
	case "partial-first":
		// 先创建空的最终路径文件，再延迟原子发布：文件存在不等于记录完整。
		return body + "open(path, 'w').close()\ntime.sleep(0.3)\n" + record
	case "empty-record":
		return body + "publish({})\n"
	case "tampered-argv":
		return body + "publish({'pipe_closed': closed, 'argv': ['--bootstrap-fd', '4']})\n"
	case "leak-fd":
		return body + record
	case "never":
		return body + "os._exit(0)\n"
	}
	t.Fatalf("unknown observer mode %q", mode)
	return ""
}

type bootstrapObservation struct {
	Closed *bool    `json:"pipe_closed"`
	Args   []string `json:"argv"`
}

// awaitBootstrapObservation judges the transport observer only after a complete
// record has been published, and separates I/O, JSON decoding, descriptor-closed
// and content diagnostics. A partially published file therefore keeps the
// caller waiting instead of being read as a closed descriptor, and an omitted
// field can never be masked by a Go zero value. deadline is the caller's
// existing budget: this waits within it, never beyond it.
func awaitBootstrapObservation(path string, deadline time.Time, wantArgs []string) (bootstrapObservation, error) {
	var lastErr error = errors.New("no publish attempt observed")
	for {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			lastErr = fmt.Errorf("read published observation: %w", readErr)
		} else if !json.Valid(data) {
			lastErr = fmt.Errorf("decode observation published as %d bytes: %s", len(data), bytes.TrimSpace(data))
		} else {
			var observation bootstrapObservation
			if decodeErr := json.Unmarshal(data, &observation); decodeErr != nil {
				lastErr = fmt.Errorf("decode observation published as %d bytes: %w", len(data), decodeErr)
			} else if observation.Closed == nil {
				lastErr = fmt.Errorf("observation omits pipe_closed, so a default value would mask the descriptor state: %s", bytes.TrimSpace(data))
			} else if !*observation.Closed {
				return observation, fmt.Errorf("bootstrap descriptor still open in the transport observer: %s", bytes.TrimSpace(data))
			} else if !slices.Equal(observation.Args, wantArgs) {
				return observation, fmt.Errorf("observation argv mismatch: got %q want %q", observation.Args, wantArgs)
			} else {
				return observation, nil
			}
		}
		if !time.Now().Before(deadline) {
			return bootstrapObservation{}, fmt.Errorf("observation incomplete before its deadline: %w", lastErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// observerReadyFileName is the handshake marker the observer helper publishes
// before touching FD 3. observerReadyBudget bounds the launch preparation
// phase on its own, so interpreter startup latency can never masquerade as a
// "never published" or wrong-content refusal inside the judgement window.
const (
	observerReadyFileName = "observer.ready.json"
	observerReadyBudget   = 5 * time.Second
)

func awaitObserverReady(path string, deadline time.Time) error {
	var lastErr error = errors.New("ready marker missing")
	for {
		data, readErr := os.ReadFile(path)
		if readErr == nil && json.Valid(data) {
			return nil
		}
		if readErr != nil {
			lastErr = readErr
		} else {
			lastErr = fmt.Errorf("ready marker published as invalid JSON: %d bytes", len(data))
		}
		if !time.Now().Before(deadline) {
			return lastErr
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// runBootstrapObserver starts the transport observer in the given publish mode,
// waits for the ready handshake within its own bounded preparation budget, and
// returns the state directory, the observation path, the argv the source launch
// must carry, and the measured preparation wait. Every caller's judgement
// window therefore starts after a demonstrably launched observer, so
// interpreter cold-start can never masquerade as a missing or late publish —
// in the Refuses negative controls nor in the partial/delayed/atomic positive
// budgets.
func runBootstrapObserver(t *testing.T, mode string) (string, string, []string, time.Duration) {
	t.Helper()
	state := t.TempDir()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	observedPath := filepath.Join(state, "observed.json")
	readyPath := filepath.Join(state, observerReadyFileName)
	script := filepath.Join(state, "capture-source")
	if err = os.WriteFile(script, []byte(bootstrapObserverScript(t, python, observedPath, readyPath, mode)), 0700); err != nil {
		t.Fatal(err)
	}
	b := hostBootstrap{Version: 2, SocketPath: filepath.Join(state, "coordinator.sock"), SpoolRoot: filepath.Join(state, "spool"), ProducerID: "launch", Hello: contract.HostHello{LaunchID: "launch", LaunchToken: "transport-secret-sentinel"}}
	if err = startSourceHost(script, state, b); err != nil {
		t.Fatal(err)
	}
	prepareStart := time.Now()
	if err = awaitObserverReady(readyPath, prepareStart.Add(observerReadyBudget)); err != nil {
		t.Fatalf("observer not ready within its bounded preparation budget: %v", err)
	}
	return state, observedPath, []string{"source-host", "--state-dir", state, "--bootstrap-fd", "3"}, time.Since(prepareStart)
}

func TestBootstrapSourceLaunchPipeExcludesSecretsFromArgumentsEnvironmentAndLog(t *testing.T) {
	state, observedPath, wantArgs, _ := runBootstrapObserver(t, "atomic")
	observation, err := awaitBootstrapObservation(observedPath, time.Now().Add(5*time.Second), wantArgs)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Closed == nil || !*observation.Closed {
		t.Fatalf("observer reported the bootstrap descriptor not closed: %+v", observation)
	}
	log, err := os.ReadFile(filepath.Join(state, "source-host.log"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(log, []byte("transport-secret-sentinel")) || len(log) != 0 {
		t.Fatalf("transport helper wrote unexpected log (%d bytes)", len(log))
	}
}

// TestBootstrapObservationPartialPublishIsNotJudgedComplete exercises an empty
// final-path file followed by an atomic complete record. File existence alone
// must not finish observation before the complete JSON is published.
func TestBootstrapObservationPartialPublishIsNotJudgedComplete(t *testing.T) {
	state, observedPath, wantArgs, _ := runBootstrapObserver(t, "partial-first")
	if _, err := awaitBootstrapObservation(observedPath, time.Now().Add(5*time.Second), wantArgs); err != nil {
		t.Fatalf("partial publish must not be judged complete: %v", err)
	}
	readyInfo, err := os.Stat(filepath.Join(state, observerReadyFileName))
	if err != nil {
		t.Fatalf("ready marker missing from the observer's private temp dir: %v", err)
	}
	// 半发布→完整发布的延迟以观察者自身 ready 发布时刻为锚：解释器冷启动与
	// 父端轮询粒度都不能冒充或被计入这 300ms 注入延迟窗。
	if delay := time.Since(readyInfo.ModTime()); delay < 300*time.Millisecond || delay > 5*time.Second {
		t.Fatalf("expected to wait for the complete publish after the ready handshake, delay=%s", delay)
	}
}

func TestBootstrapObservationDelayedAtomicPublishCompletesWithinDeadline(t *testing.T) {
	_, observedPath, wantArgs, _ := runBootstrapObserver(t, "delayed")
	start := time.Now()
	if _, err := awaitBootstrapObservation(observedPath, time.Now().Add(5*time.Second), wantArgs); err != nil {
		t.Fatalf("delayed atomic publish: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("waited beyond the original deadline, elapsed=%s", elapsed)
	}
}

func TestBootstrapObservationRefusesIncompleteOrFalseReports(t *testing.T) {
	for _, tc := range []struct{ mode, wantError string }{
		{"leak-fd", "still open"},
		{"never", "read published observation"},
		{"empty-record", "omits pipe_closed"},
		{"tampered-argv", "argv mismatch"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			_, observedPath, wantArgs, readyWait := runBootstrapObserver(t, tc.mode)
			start := time.Now()
			// 负控制使用比正向路径更短的自有截止：证明拒绝来自诊断，而不是超时被拉长。
			// 判定窗从 runBootstrapObserver 的有界 ready 握手之后才起算。
			if _, err := awaitBootstrapObservation(observedPath, time.Now().Add(1500*time.Millisecond), wantArgs); err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("%s must be refused with %q: %v", tc.mode, tc.wantError, err)
			}
			judgement := time.Since(start)
			if judgement > 5*time.Second {
				t.Fatalf("refusal exceeded the original deadline, elapsed=%s", judgement)
			}
			if tc.mode == "never" {
				if _, statErr := os.Stat(observedPath); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("mode %s must not publish anything: %v", tc.mode, statErr)
				}
			}
			t.Logf("phases: ready_wait=%s judgement=%s want=%q", readyWait.Round(time.Millisecond), judgement.Round(time.Millisecond), tc.wantError)
		})
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
