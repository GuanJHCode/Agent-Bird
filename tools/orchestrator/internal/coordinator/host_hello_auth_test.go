package coordinator

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func testHostIdentity(t *testing.T) (int, string, string) {
	t.Helper()
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	return os.Getpid(), birth, executable
}

func testRetiredOwner(t *testing.T) (int, string) {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	birth, err := process.Birth(pid)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if err != nil {
		t.Fatal(err)
	}
	return pid, birth
}

func TestHostHelloAuthenticatesPeerBeforeWrites(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS peer identity")
	}
	for _, mode := range []string{"valid", "valid_independent_owner", "valid_rebind", "valid_reconnect", "live_idle_host", "registered_worker_peer", "registered_worker_ancestor", "forged_pid", "forged_birth", "forged_executable", "exited_owner", "forged_live_owner", "bad_token_before_rebind", "exited_owner_before_rebind"} {
		t.Run(mode, func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "hello-auth-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			s, err := NewServer(filepath.Join(root, "state"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { _ = s.Serve(ctx) }()
			t.Cleanup(func() { _ = s.Close() })
			pid, birth, executable := testHostIdentity(t)
			ownerPID, ownerBirth := pid, birth
			if mode == "valid_independent_owner" {
				cmd := exec.Command("/bin/sleep", "60")
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
				ownerPID = cmd.Process.Pid
				ownerBirth, err = process.Birth(ownerPID)
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "exited_owner" || mode == "forged_live_owner" || mode == "exited_owner_before_rebind" {
				ownerPID, ownerBirth = testRetiredOwner(t)
			}
			if mode == "forged_executable" {
				executable = "/bin/sleep"
			}
			receipt, err := s.db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: ownerPID, OriginBirth: ownerBirth}, Host: store.HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: executable}, Tasks: []store.TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 1}}})
			if err != nil {
				t.Fatal(err)
			}
			hello := contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: ownerPID, OriginBirth: ownerBirth, HostGeneration: "gen", PID: pid, Birth: birth, Executable: executable}
			if mode == "valid_reconnect" || mode == "valid_rebind" || mode == "bad_token_before_rebind" || mode == "exited_owner_before_rebind" {
				old := hello
				if mode != "valid_reconnect" {
					old.PID, old.Birth = testRetiredOwner(t)
				}
				hostID, err := s.db.RegisterHost(ctx, old, s.epoch)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.db.ClaimReady(ctx, hostID, s.epoch, 1); err != nil {
					t.Fatal(err)
				}
				if err = s.db.MarkHostOffline(ctx, hostID, s.epoch); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "live_idle_host" {
				oldProcess := exec.Command("/bin/sleep", "60")
				if err := oldProcess.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = oldProcess.Process.Kill(); _ = oldProcess.Wait() })
				old := hello
				old.PID = oldProcess.Process.Pid
				old.Birth, err = process.Birth(old.PID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.db.RegisterHost(ctx, old, s.epoch); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "registered_worker_peer" || mode == "registered_worker_ancestor" {
				other, err := s.db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "other-run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "other-origin", OriginPID: ownerPID, OriginBirth: ownerBirth}, Host: store.HostLaunchSpec{OriginContextID: "other-origin", HostGeneration: "other-gen", Executable: executable}, Tasks: []store.TaskSpec{{ID: "other-task", RunID: "other-run", MaxAttempts: 1}}})
				if err != nil {
					t.Fatal(err)
				}
				otherHello := hello
				otherHello.LaunchID = other.LaunchID
				otherHello.LaunchToken = other.LaunchToken
				otherHello.OriginContextID = "other-origin"
				otherHello.HostGeneration = "other-gen"
				if mode == "registered_worker_ancestor" {
					otherHello.PID = os.Getppid()
					otherHello.Birth, err = process.Birth(otherHello.PID)
					if err != nil {
						t.Fatal(err)
					}
				}
				if _, err = s.db.RegisterHost(ctx, otherHello, s.epoch); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "forged_pid":
				hello.PID, hello.Birth = testRetiredOwner(t)
			case "forged_birth":
				hello.Birth = "forged-birth"
			case "forged_live_owner":
				hello.OriginPID, hello.OriginBirth = pid, birth
			case "bad_token_before_rebind":
				hello.LaunchToken = "fake-invalid-token"
			}
			before := hostHelloDBSnapshot(t, s)
			conn, err := net.Dial("unix", s.SocketPath())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			writeMessage(t, conn, ipc.KindHostHello, "hello", 0, hello)
			response := readMessage(t, conn)
			if mode == "valid" || mode == "valid_independent_owner" || mode == "valid_rebind" || mode == "valid_reconnect" {
				if response.Kind != ipc.KindHostReady {
					t.Fatalf("valid peer rejected: %s", response.Kind)
				}
				return
			}
			if response.Kind != ipc.KindError {
				t.Fatalf("untrusted %s accepted: %s", mode, response.Kind)
			}
			var failure ErrorResponse
			decodePayload(t, response, &failure)
			if failure.Error != string(store.ErrHostRejected) {
				t.Fatalf("error=%q", failure.Error)
			}
			if after := hostHelloDBSnapshot(t, s); after != before {
				t.Fatalf("rejected hello changed persistent host/launch/segment state")
			}
		})
	}
}

func hostHelloDBSnapshot(t *testing.T, s *Server) string {
	t.Helper()
	var result []any
	for _, table := range []string{"runtime_hosts", "host_launches", "segment_runtime"} {
		rows, err := s.db.SQL().Query("SELECT * FROM " + table)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(cols))
			args := make([]any, len(cols))
			for i := range values {
				args[i] = &values[i]
			}
			if err = rows.Scan(args...); err != nil {
				t.Fatal(err)
			}
			result = append(result, values)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A separate process owns the Host socket. Keeping controller and Host identities
// separate exercises the production worker-ancestry guard without bypassing it.
func testHostSocketPeer(t *testing.T, socket string) (net.Conn, int, string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestHostSocketPeerProcess$")
	cmd.Env = append(os.Environ(), "ORCHESTRATOR_TEST_HOST_SOCKET="+socket)
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
	birth, err := process.Birth(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return peer, cmd.Process.Pid, birth
}

func TestHostSocketPeerProcess(t *testing.T) {
	socket := os.Getenv("ORCHESTRATOR_TEST_HOST_SOCKET")
	if socket == "" {
		return
	}
	conn, err := net.Dial("unix", socket)
	if err != nil {
		os.Exit(2)
	}
	go func() { _, _ = io.Copy(conn, os.Stdin); _ = conn.Close() }()
	_, _ = io.Copy(os.Stdout, conn)
	os.Exit(0)
}
