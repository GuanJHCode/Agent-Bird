package coordinator

import (
	"context"
	"crypto/ed25519"
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
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// This is a real Host process: its PID/birth are authenticated from the Unix
// peer before Prepare.  The parent retains the original Cmd and its Wait.
func TestBoundOwnerAbandonmentHostChild(t *testing.T) {
	if os.Getenv("BOUND_ABANDONMENT_HOST_CHILD") != "1" {
		t.Skip("bound-abandonment Host helper")
	}
	var hello contract.HostHello
	if err := json.Unmarshal([]byte(os.Getenv("BOUND_ABANDONMENT_HOST_HELLO")), &hello); err != nil {
		t.Fatal(err)
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	hello.PID, hello.Birth, hello.Executable = os.Getpid(), birth, os.Args[0]
	conn, err := net.Dial("unix", os.Getenv("BOUND_ABANDONMENT_SOCKET"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	body, _ := json.Marshal(hello)
	if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostHello, RequestID: "bound-abandonment-host", Payload: body}); err != nil {
		t.Fatal(err)
	}
	frame, err := ipc.Read(conn)
	if err != nil || frame.Kind != ipc.KindOwnerHostPending {
		t.Fatal("HostHello", frame.Kind, err)
	}
	token := strings.Repeat("e", 64)
	hash, _ := contract.OriginalOwnerBootstrapSHA256(token)
	body, _ = json.Marshal(contract.OwnerPrepareRequest{Version: 1, BootstrapID: "bound-abandonment-bootstrap", BootstrapTokenSHA256: hash, StartupDeadlineUnixMS: time.Now().Add(20 * time.Second).UnixMilli()})
	if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerPrepare, RequestID: "bound-abandonment-prepare", Epoch: frame.Epoch, Payload: body}); err != nil {
		t.Fatal(err)
	}
	frame, err = ipc.Read(conn)
	if err != nil || frame.Kind != ipc.KindResponse {
		t.Fatal("OwnerPrepare", frame.Kind, err)
	}
	if err = os.WriteFile(os.Getenv("BOUND_ABANDONMENT_PREPARED"), frame.Payload, 0600); err != nil {
		t.Fatal(err)
	}
	select {}
}

func TestBoundOwnerAbandonmentPublicResumeAfterActualOwnerAndHostWait(t *testing.T) {
	s, hello := successorTransportFixture(t)
	ctx := context.Background()
	marker := filepath.Join(t.TempDir(), "prepared.json")
	helloJSON, err := json.Marshal(hello)
	if err != nil {
		t.Fatal(err)
	}
	hostCmd := exec.Command(os.Args[0], "-test.run=^TestBoundOwnerAbandonmentHostChild$", "-test.v")
	hostCmd.Env = append(os.Environ(), "BOUND_ABANDONMENT_HOST_CHILD=1", "BOUND_ABANDONMENT_HOST_HELLO="+string(helloJSON), "BOUND_ABANDONMENT_SOCKET="+s.SocketPath(), "BOUND_ABANDONMENT_PREPARED="+marker)
	hostCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = hostCmd.Start(); err != nil {
		t.Fatal(err)
	}
	hostBirth, err := process.Birth(hostCmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	var prepared contract.OwnerPrepared
	for end := time.Now().Add(5 * time.Second); ; {
		raw, readErr := os.ReadFile(marker)
		if readErr == nil && contract.DecodeOwnerRuntimeStrict(raw, &prepared) == nil {
			break
		}
		if time.Now().After(end) {
			_ = hostCmd.Process.Kill()
			_, _ = hostCmd.Process.Wait()
			t.Fatal("real Host did not Prepare", readErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	ownerCmd := exec.Command("/bin/sleep", "30")
	if err = ownerCmd.Start(); err != nil {
		t.Fatal(err)
	}
	ownerBirth, err := process.Birth(ownerCmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, cmd := range []*exec.Cmd{ownerCmd, hostCmd} {
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_, _ = cmd.Process.Wait()
			}
		}
	})
	pin, err := process.ExecutableDigest(ctx, hello.Executable)
	if err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed([]byte(strings.Repeat("n", 32)))
	owner := contract.OriginalOwnerIdentity{PID: ownerCmd.Process.Pid, Birth: ownerBirth, PublicKey: hex.EncodeToString(key.Public().(ed25519.PublicKey))}
	auth := store.OriginalOwnerAuthentication{Owner: owner, OriginPID: hello.OriginPID, OriginBirth: hello.OriginBirth, ExecutableSHA256: pin}
	bound, err := s.db.BindOriginalOwner(ctx, store.BindOriginalOwnerSpec{Hello: contract.OriginalOwnerHello{Scope: prepared.Intent.Scope, BootstrapID: prepared.Intent.BootstrapID, BootstrapToken: strings.Repeat("e", 64), Owner: owner, FirstHost: contract.OriginalOwnerTransport{PID: hostCmd.Process.Pid, Birth: hostBirth}, CoordinatorKeyID: prepared.Intent.CoordinatorKeyID}, Host: contract.HostHello{LaunchID: hello.LaunchID, LaunchToken: hello.LaunchToken, OriginContextID: hello.OriginContextID, OriginPID: hello.OriginPID, OriginBirth: hello.OriginBirth, HostGeneration: hello.HostGeneration, PID: hostCmd.Process.Pid, Birth: hostBirth, Executable: hello.Executable}, Epoch: s.epoch, Authenticated: auth}, s.ownerKeys.sign)
	if err != nil || bound.Permit.ControlRevision != 1 {
		t.Fatal("bound never-applied successor", err)
	}
	path, _ := successorStartupPriorLedger(t, s, hello)
	if err = ownerCmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	ownerWait := func() error { return ownerCmd.Wait() }()
	if ownerCmd.ProcessState == nil || ownerCmd.ProcessState.ExitCode() == 0 || ownerWait == nil {
		t.Fatal("original Owner Cmd.Wait did not observe termination", ownerWait, ownerCmd.ProcessState)
	}
	if err = hostCmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	hostWait := func() error { return hostCmd.Wait() }()
	if hostCmd.ProcessState == nil || hostCmd.ProcessState.ExitCode() == 0 || hostWait == nil {
		t.Fatal("original Host Cmd.Wait did not observe termination", hostWait, hostCmd.ProcessState)
	}
	req := ResumeRequest{TaskControlRequest: TaskControlRequest{TaskID: "owner-runtime-task", ControllerThread: "controller", ControlToken: strings.Repeat("d", 64), WorkRevision: 1}, CommandID: "bound-abandonment-resume"}
	body, _ := json.Marshal(req)
	resumeCtx := context.WithValue(context.Background(), peerAncestryKey{}, map[int]string{hello.OriginPID: hello.OriginBirth})
	response, err := s.control(resumeCtx, ipc.KindResume, body)
	if err != nil {
		t.Fatal("public Resume rejected actual exited bound successor", err)
	}
	var got ResumeResponse
	if err = json.Unmarshal(response, &got); err != nil || got.CommandID != req.CommandID || got.Mode != "successor" {
		t.Fatal("public Resume receipt", string(response), got, err)
	}
	if raw, err := os.ReadFile(path); err != nil || len(raw) == 0 {
		t.Fatal("held predecessor ledger missing after handoff", err)
	}
	t.Logf("actual Owner Cmd.Wait=%v state=%v; Host Cmd.Wait=%v state=%v; public Resume=%s", ownerWait, ownerCmd.ProcessState, hostWait, hostCmd.ProcessState, response)
}
