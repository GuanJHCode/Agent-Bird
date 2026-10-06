package coordinator

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// This real socket peer keeps its OS identity while its connection is replaced.
// Control signals never replace HostHello authentication or reconciliation.
func t05ReconnectPeer(t *testing.T, socket, marker string) (net.Conn, *exec.Cmd, string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestT05ReconnectPeerProcess$")
	cmd.Env = append(os.Environ(), "T05_RECONNECT_SOCKET="+socket, "T05_RECONNECT_MARKER="+marker)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	peer, relay := net.Pipe()
	go func() { _, _ = io.Copy(stdin, relay); _ = stdin.Close() }()
	go func() { _, _ = io.Copy(relay, stdout); _ = relay.Close() }()
	t.Cleanup(func() { _ = peer.Close(); _ = relay.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	t05WaitMarker(t, marker, "connected")
	birth, err := process.Birth(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return peer, cmd, birth
}

func t05WaitMarker(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		body, err := os.ReadFile(path)
		if err == nil && string(body) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("peer transition not observed: want=%s body=%q err=%v", want, body, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestT05ReconnectPeerProcess(t *testing.T) {
	socket := os.Getenv("T05_RECONNECT_SOCKET")
	if socket == "" {
		return
	}
	marker := os.Getenv("T05_RECONNECT_MARKER")
	controls := make(chan os.Signal, 2)
	signal.Notify(controls, syscall.SIGUSR1, syscall.SIGUSR2)
	defer signal.Stop(controls)
	var mu sync.Mutex
	var outputMu sync.Mutex
	var conn net.Conn
	var readerDone chan struct{}
	connect := func() {
		c, err := net.Dial("unix", socket)
		if err != nil {
			os.Exit(2)
		}
		mu.Lock()
		conn = c
		mu.Unlock()
		readerDone = make(chan struct{})
		go func(done chan struct{}) {
			defer close(done)
			for {
				e, err := ipc.Read(c)
				if err != nil {
					return
				}
				outputMu.Lock()
				err = ipc.Write(os.Stdout, e)
				outputMu.Unlock()
				if err != nil {
					return
				}
			}
		}(readerDone)
		if os.WriteFile(marker, []byte("connected"), 0600) != nil {
			os.Exit(3)
		}
	}
	connect()
	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		for {
			e, err := ipc.Read(os.Stdin)
			if err != nil {
				return
			}
			mu.Lock()
			c := conn
			mu.Unlock()
			if c == nil || ipc.Write(c, e) != nil {
				return
			}
		}
	}()
	for {
		select {
		case <-inputDone:
			os.Exit(0)
		case sig := <-controls:
			if sig == syscall.SIGUSR2 {
				mu.Lock()
				c := conn
				conn = nil
				mu.Unlock()
				if c != nil {
					_ = c.Close()
					<-readerDone
				}
				if os.WriteFile(marker, []byte("disconnected"), 0600) != nil {
					os.Exit(4)
				}
			} else {
				connect()
			}
		}
	}
}

func TestCrossHostDisconnectReconnectFiltersReadyWithoutLostWakeup(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "t05-reconnect-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	server, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(ctx) }()
	t.Cleanup(func() { _ = server.Close() })
	if err := server.db.ConfigureConcurrency(ctx, json.RawMessage(`{"version":1,"global":1}`)); err != nil {
		t.Fatal(err)
	}
	pid, birth, executable := testHostIdentity(t)
	seedOwner := bindOwnerForTest(t, server, "seed-owner", pid, birth)
	seedReceipt := submitReceipt(t, server, ipc.KindSubmit, seedOwner.submitRequest("seed-run", executable, []TaskRequest{{ID: "seed-task", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"fake"}`)}}))
	seedConn, seedPID, seedBirth := testHostSocketPeer(t, server.SocketPath())
	seedReady := registerHost(t, seedConn, seedReceipt, seedOwner, executable, seedPID, seedBirth)
	seed := readLaunch(t, seedConn)
	var seedSequence int64
	sendProcessEvents(t, seedConn, seedReady, seed, &seedSequence, seedPID, seedBirth)

	olderOwner := bindOwnerForTest(t, server, "older-owner", pid, birth)
	olderReceipt := submitReceipt(t, server, ipc.KindSubmit, olderOwner.submitRequest("older-run", executable, []TaskRequest{{ID: "older-task", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"fake"}`)}}))
	marker := filepath.Join(root, "peer-state")
	olderConn, peer, peerBirth := t05ReconnectPeer(t, server.SocketPath(), marker)
	registerHost(t, olderConn, olderReceipt, olderOwner, executable, peer.Process.Pid, peerBirth)
	expectNoMessage(t, olderConn, "older must wait for seed slot")
	if err := peer.Process.Signal(syscall.SIGUSR2); err != nil {
		t.Fatal(err)
	}
	t05WaitMarker(t, marker, "disconnected")
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, err := server.db.HostLaunchStatus(ctx, olderReceipt.LaunchID, olderReceipt.LaunchToken)
		if err != nil {
			t.Fatal(err)
		}
		if status == "offline" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Host did not become offline: %s", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	newerOwner := bindOwnerForTest(t, server, "newer-owner", pid, birth)
	newerReceipt := submitReceipt(t, server, ipc.KindSubmit, newerOwner.submitRequest("newer-run", executable, []TaskRequest{{ID: "newer-task", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"fake"}`)}}))
	newerConn, newerPID, newerBirth := testHostSocketPeer(t, server.SocketPath())
	newerReady := registerHost(t, newerConn, newerReceipt, newerOwner, executable, newerPID, newerBirth)
	expectNoMessage(t, newerConn, "newer must wait for seed slot")
	sendTerminalEvents(t, seedConn, seedReady, seed, &seedSequence)
	newer := readLaunch(t, newerConn)
	if newer.TaskID != "newer-task" {
		t.Fatalf("offline older starved newer: %+v", newer)
	}
	var newerSequence int64
	sendProcessEvents(t, newerConn, newerReady, newer, &newerSequence, newerPID, newerBirth)
	if err := peer.Process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	t05WaitMarker(t, marker, "connected")
	olderReady := registerHost(t, olderConn, olderReceipt, olderOwner, executable, peer.Process.Pid, peerBirth)
	expectNoMessage(t, olderConn, "reconnected older must not steal occupied slot")
	sendTerminalEvents(t, newerConn, newerReady, newer, &newerSequence)
	older := readLaunch(t, olderConn)
	if older.TaskID != "older-task" {
		t.Fatalf("reconnected Host not awakened: %+v", older)
	}
	var olderSequence int64
	sendProcessEvents(t, olderConn, olderReady, older, &olderSequence, peer.Process.Pid, peerBirth)
	sendTerminalEvents(t, olderConn, olderReady, older, &olderSequence)
}
