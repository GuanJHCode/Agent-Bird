package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type rebindPublicationChild struct {
	generation string
	command    *exec.Cmd
	done       chan struct{}
	waitErr    error
}

const (
	rebindPublicationStateEnv      = "ORCHESTRATOR_REBIND_PUBLICATION_STATE"
	rebindPublicationRequestEnv    = "ORCHESTRATOR_REBIND_PUBLICATION_REQUEST"
	rebindPublicationGenerationEnv = "ORCHESTRATOR_REBIND_PUBLICATION_GENERATION"
)

// TestRebindPublicationChild is deliberately invoked through the test binary.
// Normal package tests skip it; the parent exercises the actual CLI function in
// separate processes, with no duplicate rebind implementation in the fixture.
func TestRebindPublicationChild(t *testing.T) {
	state, request, generation := os.Getenv(rebindPublicationStateEnv), os.Getenv(rebindPublicationRequestEnv), os.Getenv(rebindPublicationGenerationEnv)
	if state == "" || request == "" || generation == "" {
		t.Skip("helper process only")
	}
	if _, err := fmt.Fprintf(os.Stdout, "BEGIN %s\n", generation); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := rebindOwner(ctx, []string{"--state-dir", state, "--request", request}, os.Stdout); err != nil {
		t.Fatalf("rebind %s: %v", generation, err)
	}
}

func TestRebindPublicationSerializesCLIProcesses(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	bootstrapDir := filepath.Join(state, "host-bootstrap")
	if err := os.Mkdir(bootstrapDir, 0700); err != nil {
		t.Fatal(err)
	}
	bootstrapPath := filepath.Join(bootstrapDir, "launch.json")
	bootstrap := hostBootstrap{
		Version:    2,
		SocketPath: filepath.Join(state, "coordinator.sock"),
		SpoolRoot:  filepath.Join(state, "host-spool", "launch"),
		ProducerID: "launch",
		Hello: contract.HostHello{
			LaunchID: "launch", LaunchToken: "launch-token", OriginContextID: "origin",
			OriginPID: 1, OriginBirth: "original-birth", HostGeneration: "original-generation",
		},
	}
	if err := writeExclusiveJSON(bootstrapPath, bootstrapMetadata(bootstrap)); err != nil {
		t.Fatal(err)
	}
	controlPath := filepath.Join(root, "control.json")
	if err := writeExclusiveJSON(controlPath, controlCapability{
		Version: 2, RunID: "run", ControllerThread: "controller", ControlToken: "control-token",
		BootstrapPath: bootstrapPath, HostLaunchID: "launch", HostLaunchToken: "launch-token",
	}); err != nil {
		t.Fatal(err)
	}
	requests := make(map[string]string)
	for _, generation := range []string{"G1", "G2"} {
		ownerPath := filepath.Join(root, "owner-"+generation+".json")
		owner := store.OwnerGrant{Version: 1, Kind: "local-owner", ID: "owner-" + generation, Token: "token-" + generation, ControllerThread: "controller", OriginPID: 100, OriginBirth: "birth-" + generation, OriginContextID: "origin", HostGeneration: generation}
		if err := writeExclusiveJSON(ownerPath, owner); err != nil {
			t.Fatal(err)
		}
		requestPath := filepath.Join(root, "request-"+generation+".json")
		if err := writeExclusiveJSON(requestPath, map[string]any{"version": 1, "owner_mode": "local", "owner_capability": ownerPath, "control_file": controlPath}); err != nil {
			t.Fatal(err)
		}
		requests[generation] = requestPath
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	listener, err := net.Listen("unix", filepath.Join(state, "coordinator.sock"))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	g1Arrived := make(chan struct{})
	releaseG1 := make(chan struct{})
	violation := make(chan struct{}, 1)
	g1Seen, g1Released := false, false
	var onceG1, onceRelease sync.Once
	release := func() {
		onceRelease.Do(func() {
			mu.Lock()
			g1Released = true
			mu.Unlock()
			close(releaseG1)
		})
	}
	var handlers sync.WaitGroup
	serveConn := func(conn net.Conn) {
		defer handlers.Done()
		defer conn.Close()
		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(deadline)
		}
		stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stopClose()
		message, readErr := ipc.Read(conn)
		if readErr != nil {
			return
		}
		if message.Kind == ipc.KindReady {
			_ = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindResponse, RequestID: message.RequestID, Epoch: 1, Payload: json.RawMessage(`{"status":"ready","epoch":1}`)})
			return
		}
		if message.Kind != ipc.KindRebindOwner {
			return
		}
		var request coordinator.RebindOwnerRequest
		if json.Unmarshal(message.Payload, &request) != nil {
			return
		}
		if request.HostGeneration == "G1" {
			onceG1.Do(func() {
				mu.Lock()
				g1Seen = true
				mu.Unlock()
				close(g1Arrived)
			})
			select {
			case <-releaseG1:
			case <-ctx.Done():
				return
			}
		} else if request.HostGeneration == "G2" {
			mu.Lock()
			early := !g1Released
			mu.Unlock()
			if early {
				select {
				case violation <- struct{}{}:
				default:
				}
			}
		} else {
			return
		}
		payload, _ := json.Marshal(map[string]any{"version": 1, "status": "owner_rebound", "run_id": "run", "origin_context_id": "origin", "host_generation": request.HostGeneration})
		_ = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindResponse, RequestID: message.RequestID, Epoch: 1, Payload: payload})
	}
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			handlers.Add(1)
			go serveConn(conn)
		}
	}()
	children := make([]*rebindPublicationChild, 0, 2)
	handlersDone := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		release()
		for _, child := range children {
			if child.command.Process != nil {
				_ = child.command.Process.Kill()
			}
		}
		_ = listener.Close()
		select {
		case <-acceptDone:
		case <-time.After(2 * time.Second):
			t.Errorf("fake coordinator accept loop did not stop")
		}
		go func() {
			handlers.Wait()
			close(handlersDone)
		}()
		select {
		case <-handlersDone:
		case <-time.After(2 * time.Second):
			t.Errorf("fake coordinator handlers did not stop")
		}
		for _, child := range children {
			select {
			case <-child.done:
				t.Logf("child %s cleanup wait=%v exit=%d", child.generation, child.waitErr, child.command.ProcessState.ExitCode())
			case <-time.After(2 * time.Second):
				t.Errorf("child %s did not exit during cleanup", child.generation)
			}
		}
	})

	start := func(generation string, stdout *os.File) *rebindPublicationChild {
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRebindPublicationChild$")
		command.Env = append(os.Environ(), rebindPublicationStateEnv+"="+state, rebindPublicationRequestEnv+"="+requests[generation], rebindPublicationGenerationEnv+"="+generation)
		command.Stdout = stdout
		command.Stderr = os.Stderr
		if err := command.Start(); err != nil {
			t.Fatalf("start %s: %v", generation, err)
		}
		child := &rebindPublicationChild{generation: generation, command: command, done: make(chan struct{})}
		children = append(children, child)
		go func() {
			child.waitErr = command.Wait()
			close(child.done)
		}()
		return child
	}
	// G1 need not be observed through stdout; the coordinator's arrival is the
	// ordering point that makes the G2 observation deterministic.
	child1 := start("G1", os.Stdout)
	select {
	case <-g1Arrived:
	case <-ctx.Done():
		t.Fatalf("G1 did not reach fake coordinator: %v", ctx.Err())
	}
	mu.Lock()
	seen := g1Seen
	mu.Unlock()
	if !seen {
		t.Fatal("G1 arrival channel closed without G1")
	}
	reader2, writer2, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader2.Close() })
	t.Cleanup(func() { _ = writer2.Close() })
	child2 := start("G2", writer2)
	_ = writer2.Close()
	scanner := bufio.NewScanner(reader2)
	begin2 := make(chan string, 1)
	outputDone := make(chan struct{})
	var output2 string
	var output2Err error
	go func() {
		if scanner.Scan() {
			begin2 <- scanner.Text()
		} else {
			begin2 <- ""
		}
		var remaining []string
		for scanner.Scan() {
			remaining = append(remaining, scanner.Text())
		}
		output2, output2Err = strings.Join(remaining, "\n"), scanner.Err()
		close(outputDone)
	}()
	select {
	case line := <-begin2:
		if line != "BEGIN G2" {
			t.Fatalf("G2 did not announce begin: %q", line)
		}
	case <-ctx.Done():
		t.Fatalf("G2 did not begin: %v", ctx.Err())
	}
	earlyG2 := false
	select {
	case <-violation:
		earlyG2 = true
		release()
	case <-time.After(200 * time.Millisecond):
		release()
	}
	wait := func(child *rebindPublicationChild) error {
		select {
		case <-child.done:
			t.Logf("child %s wait=%v exit=%d", child.generation, child.waitErr, child.command.ProcessState.ExitCode())
			return child.waitErr
		case <-ctx.Done():
			return fmt.Errorf("%s wait: %w", child.generation, ctx.Err())
		}
	}
	err1, err2 := wait(child1), wait(child2)
	select {
	case <-outputDone:
		t.Logf("child G2 remaining stdout=%q scanner_error=%v", output2, output2Err)
	case <-ctx.Done():
		t.Fatalf("G2 stdout drain: %v", ctx.Err())
	}
	if err1 != nil || err2 != nil {
		t.Fatalf("child waits: G1=%v G2=%v", err1, err2)
	}
	if earlyG2 {
		t.Fatal("G2 reached coordinator before G1 bootstrap publication was released")
	}
	var final hostBootstrapMetadataV2
	if err := readPrivateJSON(bootstrapPath, &final); err != nil {
		t.Fatal(err)
	}
	if final.Hello.HostGeneration != "G2" {
		t.Fatalf("final bootstrap generation=%q, want G2", final.Hello.HostGeneration)
	}
}
