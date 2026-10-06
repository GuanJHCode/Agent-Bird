//go:build linux

package ipc

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxOriginalAPIsPeerPIDOverUnixSocket(t *testing.T) {
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "peer.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	connected := make(chan net.Conn, 1)
	go func() {
		conn, err := net.Dial("unix", listener.Addr().String())
		if err == nil {
			connected <- conn
		}
	}()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	defer func() { <-connected }()
	pid, err := PeerPID(server)
	if err != nil || pid != os.Getpid() {
		t.Fatalf("kernel peer PID = %d, %v; want %d", pid, err, os.Getpid())
	}
	if _, err := PeerPID(pipeConn(t)); err == nil {
		t.Fatal("non-Unix connection admitted")
	}
}

func pipeConn(t *testing.T) net.Conn {
	t.Helper()
	left, right := net.Pipe()
	t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
	return left
}
