package coordinator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
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
	var prepared contract.OwnerPrepared
	if err = contract.DecodeOwnerRuntimeStrict(frame.Payload, &prepared); err != nil {
		t.Fatal(err)
	}
	step, finish := startOwnerRPCChild(t, ownerChildConfig{Socket: os.Getenv("BOUND_ABANDONMENT_OWNER_SOCKET"), Prepared: prepared, BootstrapToken: token})
	ownerFinished := false
	finishOwned := func() {
		if !ownerFinished {
			ownerFinished = true
			finish()
		}
	}
	defer finishOwned()
	reply := step(ownerChildStep{Kind: "bind", ID: prepared.Intent.RequestID})
	if reply.Error != "" {
		t.Fatal("authenticated child Owner Bind", reply.Error)
	}
	marker := os.Getenv("BOUND_ABANDONMENT_PREPARED")
	if err = os.WriteFile(marker+".bound", reply.Body, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancel()
	for {
		if _, err = os.Stat(marker + ".finish-owner"); err == nil && !ownerFinished {
			finishOwned()
			if err = os.WriteFile(marker+".owner-exited", []byte("original Cmd.Wait=0\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func boundAbandonmentHost(t *testing.T, s *Server, hello contract.HostHello) (contract.OwnerBindReply, func(), func()) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "prepared.json")
	helloJSON, err := json.Marshal(hello)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestBoundOwnerAbandonmentHostChild$", "-test.v")
	cmd.Env = append(os.Environ(), "BOUND_ABANDONMENT_HOST_CHILD=1", "BOUND_ABANDONMENT_HOST_HELLO="+string(helloJSON), "BOUND_ABANDONMENT_SOCKET="+s.SocketPath(), "BOUND_ABANDONMENT_OWNER_SOCKET="+s.OwnerSocketPath(), "BOUND_ABANDONMENT_PREPARED="+marker)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	log, err := os.OpenFile(marker+".log", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = log, log
	if err = cmd.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	finished := false
	stop := func() {
		t.Helper()
		if finished {
			return
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-done:
			finished = true
			t.Logf("actual Host PID=%d original Cmd.Wait=%v state=%v", cmd.Process.Pid, err, cmd.ProcessState)
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			err := <-done
			finished = true
			t.Errorf("forced Host cleanup original Wait=%v", err)
		}
	}
	// Registered immediately after Start: every failure retains the sole Cmd.Wait.
	t.Cleanup(func() {
		stop()
		log.Close()
		body, _ := os.ReadFile(marker + ".log")
		t.Logf("original Host output: %s", body)
	})
	var bound contract.OwnerBindReply
	for end := time.Now().Add(5 * time.Second); ; {
		raw, readErr := os.ReadFile(marker + ".bound")
		if readErr == nil && contract.DecodeOwnerRuntimeStrict(raw, &bound) == nil {
			break
		}
		if time.Now().After(end) {
			t.Fatal("real Host did not Prepare", readErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	finishOwner := func() {
		t.Helper()
		if err := os.WriteFile(marker+".finish-owner", []byte("finish\n"), 0600); err != nil {
			t.Fatal(err)
		}
		for end := time.Now().Add(5 * time.Second); ; {
			if body, err := os.ReadFile(marker + ".owner-exited"); err == nil && string(body) == "original Cmd.Wait=0\n" {
				return
			}
			if time.Now().After(end) {
				t.Fatal("FirstHost did not retain original Owner Wait")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	return bound, finishOwner, stop
}

// Historical incarnation 1 is fixture data. Both current processes are real:
// Host Prepare authenticates the Unix peer, and Owner Bind authenticates its
// peer, executable, signed challenge and in-memory private key. No fabricated
// OriginalOwnerAuthentication or SQL death status enters the current flow.
func TestBoundOwnerAbandonmentPublicResumeAfterActualOwnerAndHostWait(t *testing.T) {
	for _, phase := range []string{"no_intent", "prepared_prior", "prepared_next", "applied_current"} {
		t.Run(phase, func(t *testing.T) {
			s, hello := successorTransportFixture(t)
			ctx := context.Background()
			bound, finishOwner, stopHost := boundAbandonmentHost(t, s, hello)
			state, err := s.db.InspectOriginalOwner(ctx, bound.Binding.Scope.OwnerID)
			if err != nil || state.AppliedControlRevision != 0 {
				t.Fatal("unexpected Applied", err)
			}
			path, priorBytes := successorStartupPriorLedger(t, s, hello)
			finishOwner()
			stopHost()
			req := ResumeRequest{TaskControlRequest: TaskControlRequest{TaskID: "owner-runtime-task", ControllerThread: "controller", ControlToken: strings.Repeat("d", 64), WorkRevision: 1}, CommandID: "bound-abandonment-" + phase}
			storeReq := store.OriginalOwnerResumeRequest{TaskID: req.TaskID, ControllerThread: req.ControllerThread, ControlToken: req.ControlToken, WorkRevision: req.WorkRevision, CommandID: req.CommandID}
			now := time.Now().UTC().Format(time.RFC3339Nano)
			observation := store.OriginalOwnerSuccessionObservation{OwnerID: state.Intent.Scope.OwnerID, Owner: contract.OriginalOwnerTransport{PID: bound.Binding.Owner.PID, Birth: bound.Binding.Owner.Birth}, Host: *state.CurrentHost, Origin: store.RunOwnerBinding{OriginContextID: hello.OriginContextID, OriginPID: hello.OriginPID, OriginBirth: hello.OriginBirth}, Epoch: s.epoch, ExecutableSHA256: state.Intent.Scope.ExecutableSHA256, OwnerExitedAt: now, HostExitedAt: now}
			if err = observeResumeExited(ctx, observation.Owner, "still_alive"); err != nil {
				t.Fatal(err)
			}
			if err = observeResumeExited(ctx, observation.Host, "still_alive"); err != nil {
				t.Fatal(err)
			}
			external, err := sql.Open("sqlite", filepath.Join(s.stateDir, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer external.Close()
			var sequence int64
			if err = external.QueryRow(`SELECT ready_sequence FROM task_runtime WHERE task_id=?`, req.TaskID).Scan(&sequence); err != nil {
				t.Fatal(err)
			}
			if phase != "no_intent" {
				resolved, err := s.db.ResolveBoundOwnerAbandonment(ctx, storeReq, observation, s.ownerPublicKey())
				if err != nil {
					t.Fatal(err)
				}
				lease, err := host.OpenExistingOwnerRecoveryHandoff(s.stateDir, resolved.PriorBinding.Scope, resolved.PriorBinding.Owner, resolved.Binding.Scope, resolved.Binding.Owner, nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(lease.Close)
				facts, err := lease.Facts()
				if err != nil {
					t.Fatal(err)
				}
				proof, err := s.db.PrepareBoundOwnerAbandonment(ctx, storeReq, observation, ownerRecoveryStoreFacts(facts), s.ownerPublicKey())
				if err != nil {
					t.Fatal(err)
				}
				if phase != "prepared_prior" {
					if err = lease.CommitPrepared(ownerRecoveryFileProof(proof)); err != nil {
						t.Fatal(err)
					}
				}
				if phase == "applied_current" {
					if _, err = s.db.ApplyBoundOwnerAbandonment(ctx, storeReq, observation, ownerRecoveryStoreFacts(facts), s.ownerPublicKey()); err != nil {
						t.Fatal(err)
					}
				}
				lease.Close()
			}
			var proofs, handoffs, applied int
			if err = external.QueryRow(`SELECT (SELECT COUNT(*) FROM owner_bound_abandonments),(SELECT COUNT(*) FROM owner_recovery_scope_handoffs),COALESCE((SELECT MAX(applied) FROM owner_recovery_scope_handoffs),0)`).Scan(&proofs, &handoffs, &applied); err != nil {
				t.Fatal(err)
			}
			expectedProofs, expectedApplied := 1, 0
			if phase == "no_intent" {
				expectedProofs = 0
			}
			if phase == "applied_current" {
				expectedApplied = 1
			}
			if proofs != expectedProofs || handoffs != expectedProofs || applied != expectedApplied {
				t.Fatal("wrong durable checkpoint", proofs, handoffs, applied)
			}
			before, _ := os.ReadFile(path)
			if (phase == "no_intent" || phase == "prepared_prior") != bytes.Equal(before, priorBytes) {
				t.Fatal("wrong FS checkpoint")
			}
			body, _ := json.Marshal(req)
			resumeCtx := context.WithValue(ctx, peerAncestryKey{}, map[int]string{hello.OriginPID: hello.OriginBirth})
			response, err := s.control(resumeCtx, ipc.KindResume, body)
			if err != nil {
				t.Fatal("public Resume", err)
			}
			var got ResumeResponse
			if json.Unmarshal(response, &got) != nil || got.CommandID != req.CommandID || got.Mode != "successor" || got.ReadySequence != sequence {
				t.Fatal("receipt changed reservation", string(response))
			}
			after, info := recoveryRPCRead(t, path)
			var oldRecord, newRecord map[string]any
			if json.Unmarshal(priorBytes, &oldRecord) != nil || json.Unmarshal(after, &newRecord) != nil {
				t.Fatal("bad ledger")
			}
			scope, _ := contract.OriginalOwnerRecoveryScopeSHA256(bound.Binding.Scope, bound.Binding.Owner)
			if newRecord["scope_sha256"] != scope {
				t.Fatal("skipped adjacent scope")
			}
			oldRecord["scope_sha256"] = scope
			oldJSON, _ := json.Marshal(oldRecord)
			newJSON, _ := json.Marshal(newRecord)
			if !bytes.Equal(oldJSON, newJSON) {
				t.Fatal("scope handoff changed authority/budget")
			}
			if replay, err := s.control(resumeCtx, ipc.KindResume, body); err != nil || !bytes.Equal(response, replay) {
				t.Fatal("exact public replay", err)
			}
			again, againInfo := recoveryRPCRead(t, path)
			if !bytes.Equal(after, again) || !os.SameFile(info, againInfo) || !info.ModTime().Equal(againInfo.ModTime()) {
				t.Fatal("reply replay rewrote file")
			}
			if err = external.QueryRow(`SELECT COUNT(*) FROM owner_succession_authorizations WHERE abandoned_owner_id=?`, observation.OwnerID).Scan(&proofs); err != nil || proofs != 1 {
				t.Fatal("not one successor authorization", proofs, err)
			}
			t.Logf("authenticated real Owner/Host exited; checkpoint=%s, preserved ready_sequence=%d, public successor receipt=%s", phase, sequence, response)
		})
	}
}
