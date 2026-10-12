package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

const (
	protocol2LossMarkerEnv = "PROTOCOL2_LOSS_MARKER"
	protocol2LossGateEnv   = "PROTOCOL2_LOSS_GATE"
)

// runProtocol2LossFixture leaves every public command and the original-owner
// route in production code. The only test seam blocks FirstHost after its
// signed Coordinator Start acknowledgement and before it writes the bootstrap
// payload or source environment to the child.
func runProtocol2LossFixture() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "public-succession-origin":
		_ = os.Setenv("OWNER_LAUNCH_SOURCE_SENTINEL", protocol2EnvironmentSentinel)
		err = runPublicRebindOrigin()
	case "source-host":
		err = publicSuccessionOwnedHostLauncher(os.Args[2:])
	case "public-succession-owned-host":
		err = protocol2LossOwnedHost(os.Args[2:])
	case "submit":
		// Stop this caller's Host-ready polling as soon as signed ACK exists.
		// The independently launched FirstHost remains gated. Waiting for the
		// normal five-second timeout can let Owner expire; leaving polling live
		// across SIGKILL would auto-start an unowned replacement Coordinator.
		ctx, cancel := context.WithCancel(context.Background())
		joined := make(chan struct{})
		go func() {
			defer close(joined)
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				if _, e := os.Stat(os.Getenv(protocol2LossMarkerEnv)); e == nil {
					cancel()
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
		err = run(ctx, os.Args[1:], os.Stdout, os.Stderr)
		cancel()
		<-joined
	case "original-owner":
		if os.Getenv("OWNER_LAUNCH_SOURCE_SENTINEL") != "" {
			err = errors.New("coordinator leaked source environment before bootstrap")
		} else {
			err = run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)
		}
	default:
		err = run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)
	}
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, `{"version":1,"status":"error","error":%q}`+"\n", errorCode(err))
		os.Exit(2)
	}
}

type protocol2LossMarker struct {
	OwnerID     string                              `json:"owner_id"`
	Attempt     contract.OriginalOwnerLaunchAttempt `json:"attempt"`
	Receipt     contract.OriginalOwnerLaunchReceipt `json:"receipt"`
	Child       contract.OriginalOwnerTransport     `json:"child"`
	Coordinator string                              `json:"coordinator_public_key"`
}

func protocol2LossOwnedHost(args []string) error {
	marker, gate := os.Getenv(protocol2LossMarkerEnv), os.Getenv(protocol2LossGateEnv)
	if marker == "" || gate == "" {
		return errors.New("loss fixture gate missing")
	}
	previous := ownerLaunchBeforePayload
	defer func() { ownerLaunchBeforePayload = previous }()
	ownerLaunchBeforePayload = func(ctx context.Context, bootstrap contract.OwnerRuntimeBootstrap) error {
		if bootstrap.Prepared.LaunchAttempt == nil || bootstrap.LaunchReceipt == nil || bootstrap.LaunchReceipt.Child == nil {
			return errors.New("loss fixture missing signed launch facts")
		}
		body, err := json.Marshal(protocol2LossMarker{
			OwnerID:     bootstrap.Prepared.Intent.Scope.OwnerID,
			Attempt:     *bootstrap.Prepared.LaunchAttempt,
			Receipt:     *bootstrap.LaunchReceipt,
			Child:       *bootstrap.LaunchReceipt.Child,
			Coordinator: bootstrap.Prepared.CoordinatorPublicKey,
		})
		if err != nil {
			return err
		}
		if err = os.WriteFile(marker, append(body, '\n'), 0600); err != nil {
			return err
		}
		for {
			if _, err = os.Stat(gate); err == nil {
				return errors.New("loss fixture released without owner payload")
			}
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	return publicSuccessionOwnedHost(args)
}

func TestPublicOwnerLaunchCoordinatorLossUnknown(t *testing.T) {
	root, err := os.MkdirTemp("/private/tmp", "owner-launch-loss-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	clean := false
	t.Cleanup(func() {
		if clean && !t.Failed() {
			_ = os.RemoveAll(root)
		} else {
			t.Log("retained coordinator-loss fixture", root)
		}
	})
	state := filepath.Join(root, "state")
	if err = os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(image)
	cli := filepath.Join(root, "protocol2-loss-cli-fixture")
	if err = os.WriteFile(cli, image, 0700); err != nil {
		t.Fatal(err)
	}
	marker, gate := filepath.Join(root, "signed-started.json"), filepath.Join(root, "release-first-host")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	t.Setenv("ORCHESTRATOR_ENABLE_TEST_FAKE", "1")
	// submit is a child of the public origin, and it launches source-host. Keep
	// these fixture-only paths in that source lineage; the Coordinator still
	// receives only its fixed minimal environment.
	t.Setenv(protocol2LossMarkerEnv, marker)
	t.Setenv(protocol2LossGateEnv, gate)
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")

	type serverHandle struct {
		cmd  *exec.Cmd
		done chan error
		log  *os.File
	}
	startServer := func() *serverHandle {
		t.Helper()
		log, e := os.OpenFile(filepath.Join(root, fmt.Sprintf("coordinator-%d.log", time.Now().UnixNano())), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		cmd := exec.Command(cli, "serve", "--state-dir", state)
		cmd.Env = append(os.Environ(), "ORCHESTRATOR_ENABLE_TEST_FAKE=1", protocol2LossMarkerEnv+"="+marker, protocol2LossGateEnv+"="+gate)
		cmd.Stdout, cmd.Stderr = log, log
		if e = cmd.Start(); e != nil {
			_ = log.Close()
			t.Fatal(e)
		}
		h := &serverHandle{cmd: cmd, done: make(chan error, 1), log: log}
		go func() { h.done <- cmd.Wait() }()
		return h
	}
	waitReady := func(label string) uint64 {
		t.Helper()
		for end := time.Now().Add(20 * time.Second); ; {
			readyCtx, stop := context.WithTimeout(ctx, 200*time.Millisecond)
			reply, e := ipc.Call(readyCtx, filepath.Join(state, "coordinator.sock"), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindReady, RequestID: "owner-launch-loss-" + label})
			stop()
			if e == nil && reply.Kind == ipc.KindResponse && reply.Epoch > 0 {
				return reply.Epoch
			}
			if time.Now().After(end) {
				t.Fatal("Coordinator did not become ready", label, e)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	first := startServer()
	firstWaited := false
	t.Cleanup(func() {
		_ = os.WriteFile(gate, nil, 0600)
		if !firstWaited {
			_ = first.cmd.Process.Signal(syscall.SIGTERM)
			select {
			case e := <-first.done:
				t.Logf("first Coordinator original Wait cleanup: %v", e)
			case <-time.After(13 * time.Second):
				_ = first.cmd.Process.Kill()
				<-first.done
				t.Error("forced first Coordinator cleanup")
			}
		}
		_ = first.log.Close()
	})

	firstEpoch := waitReady("first")
	origin := startPublicRebindOrigin(t, ctx, cli)
	birth, err := process.Birth(origin.command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, value any) string {
		t.Helper()
		path := filepath.Join(root, name)
		body, e := json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, body, 0600); e != nil {
			t.Fatal(e)
		}
		return path
	}
	callOK := func(args ...string) []byte {
		t.Helper()
		reply := origin.call(t, args)
		if reply.ExitCode != 0 {
			t.Fatalf("public %s failed exit=%d stderr=%s stdout=%s", args[0], reply.ExitCode, reply.Stderr, reply.Stdout)
		}
		return reply.Stdout
	}
	bind := write("owner-bind.json", coordinator.OwnerBindRequest{ControllerThread: "loss-controller", OriginPID: origin.command.Process.Pid, OriginBirth: birth})
	var local struct {
		OwnerCapability string `json:"owner_capability"`
		OriginContextID string `json:"origin_context_id"`
		HostGeneration  string `json:"host_generation"`
	}
	if err = json.Unmarshal(callOK("owner-bind", "--state-dir", state, "--request", bind), &local); err != nil || local.OwnerCapability == "" {
		t.Fatal("local owner bind", err)
	}
	request := map[string]any{"owner_mode": "local", "owner_capability": local.OwnerCapability, "run_id": "protocol2-loss-run", "controller_thread": "loss-controller", "plan_revision": 1, "origin_context_id": local.OriginContextID, "origin_pid": origin.command.Process.Pid, "origin_birth": birth, "host_generation": local.HostGeneration, "delivery_mode": "collect", "tasks": []any{map[string]any{"id": "worker", "max_attempts": 1, "max_active_ms": 30000, "completion_policy": "owner_review", "execution_policy": contract.ExecutionPolicy{Version: 1, GroupMaxAttempts: 1, GroupMaxActiveMS: 60000, TaskMaxActiveMS: 60000, SegmentMaxActiveMS: 30000}, "adapter": map[string]any{"kind": "fake", "args": []string{cli}, "directory": root}}}}
	submitReply := origin.call(t, []string{"submit", "--owner-launch-protocol", "2", "--state-dir", state, "--request", write("submit.json", request)})
	var submitted struct {
		Status      string `json:"status"`
		ControlFile string `json:"control_file"`
	}
	if err = json.Unmarshal(submitReply.Stdout, &submitted); err != nil || submitReply.ExitCode != 2 || submitted.Status != "queued_host_unavailable" || submitted.ControlFile == "" {
		t.Fatalf("submit did not retain public control: exit=%d stdout=%s stderr=%s err=%v", submitReply.ExitCode, submitReply.Stdout, submitReply.Stderr, err)
	}
	for end := time.Now().Add(20 * time.Second); ; {
		if _, e := os.Stat(marker); e == nil {
			break
		}
		if time.Now().After(end) {
			t.Fatal("signed pre-payload marker absent")
		}
		time.Sleep(20 * time.Millisecond)
	}
	var observed protocol2LossMarker
	raw, err := os.ReadFile(marker)
	if err != nil || json.Unmarshal(raw, &observed) != nil || observed.OwnerID == "" || observed.Attempt.Attempt != 1 || observed.Receipt.Phase != contract.OriginalOwnerLaunchStartedWaitOwned || observed.Receipt.Child == nil || observed.Child != *observed.Receipt.Child {
		t.Fatal("invalid signed pre-payload marker", err)
	}
	public, err := hex.DecodeString(observed.Coordinator)
	if err != nil || len(public) != ed25519.PublicKeySize || contract.VerifyOriginalOwnerLaunchReceipt(observed.Receipt, ed25519.PublicKey(public), observed.Attempt) != nil || observed.Receipt.HandoffClosed != true {
		t.Fatal("marker does not carry a valid signed Start acknowledgement", err)
	}
	dbURL := url.URL{Scheme: "file", Path: filepath.Join(state, "state.db"), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", dbURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	snapshot := func() string {
		t.Helper()
		var ownerPhase, headPhase, taskStatus string
		var attempts, receipts, bindings, businessAttempts, segments, grants int
		if e := db.QueryRow(`SELECT o.phase,lh.phase,t.status,(SELECT COUNT(*) FROM owner_launch_attempts WHERE owner_id=o.owner_id),(SELECT COUNT(*) FROM owner_launch_receipts WHERE owner_id=o.owner_id),(SELECT COUNT(*) FROM execution_owners WHERE original_binding_json IS NOT NULL),(SELECT COUNT(*) FROM attempts),(SELECT COUNT(*) FROM segments),(SELECT COUNT(*) FROM budget_runtime) FROM execution_owners o JOIN owner_launch_heads lh ON lh.owner_id=o.owner_id JOIN tasks t ON t.run_id=o.run_id WHERE o.owner_id=?`, observed.OwnerID).Scan(&ownerPhase, &headPhase, &taskStatus, &attempts, &receipts, &bindings, &businessAttempts, &segments, &grants); e != nil {
			t.Fatal(e)
		}
		if ownerPhase != "intended" || headPhase != contract.OriginalOwnerLaunchStartedWaitOwned || taskStatus != "ready" || attempts != 1 || receipts != 3 || bindings != 0 || businessAttempts != 0 || segments != 0 || grants != 0 {
			t.Fatal("UNKNOWN changed business or launch facts", ownerPhase, headPhase, taskStatus, attempts, receipts, bindings, businessAttempts, segments, grants)
		}
		return fmt.Sprintf("phase=%s launch_head=%s task=%s launch_attempts=%d receipts=%d bindings=%d attempts=%d segments=%d grants=%d", ownerPhase, headPhase, taskStatus, attempts, receipts, bindings, businessAttempts, segments, grants)
	}
	before := snapshot()
	if err = first.cmd.Process.Kill(); err != nil {
		t.Fatal("kill exact original Coordinator process", err)
	}
	select {
	case waitErr := <-first.done:
		firstWaited = true
		if waitErr == nil || first.cmd.ProcessState == nil || first.cmd.ProcessState.ExitCode() != -1 {
			t.Fatal("Coordinator SIGKILL was not observed by its original Cmd.Wait", waitErr, first.cmd.ProcessState)
		}
		status, ok := first.cmd.ProcessState.Sys().(syscall.WaitStatus)
		if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
			t.Fatal("original Wait did not witness SIGKILL", status)
		}
	case <-time.After(13 * time.Second):
		t.Fatal("Coordinator original Cmd.Wait did not return after SIGKILL")
	}
	if err = os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	hostWait := publicSuccessionReadWait(t, state, "first_host", observed.Attempt.EffectiveIntent.FirstHost.PID)
	if hostWait.ExitCode == 0 || hostWait.WaitError == "" {
		t.Fatal("FirstHost did not retain its original failure Wait", hostWait)
	}
	for end := time.Now().Add(13 * time.Second); ; {
		gone, e := process.IdentityExitedContext(ctx, observed.Child.PID, observed.Child.Birth)
		if e != nil {
			t.Fatal("Owner child exit observation unknown", e)
		}
		if gone {
			break
		}
		if time.Now().After(end) {
			t.Fatal("Owner child did not affirmatively exit after payload EOF", observed.Child)
		}
		time.Sleep(20 * time.Millisecond)
	}

	second := startServer()
	secondWaited := false
	t.Cleanup(func() {
		if !secondWaited {
			_ = second.cmd.Process.Signal(syscall.SIGTERM)
			select {
			case e := <-second.done:
				if e != nil {
					t.Error("restarted Coordinator graceful original Wait", e)
				}
				secondWaited = true
			case <-time.After(13 * time.Second):
				_ = second.cmd.Process.Kill()
				<-second.done
				t.Error("forced restarted Coordinator cleanup")
			}
		}
		_ = second.log.Close()
	})
	secondEpoch := waitReady("restarted")
	if secondEpoch == firstEpoch {
		t.Fatal("restarted Coordinator reused the killed instance epoch", firstEpoch)
	}
	recovery := write("recover.json", map[string]any{"version": 1, "task_id": "worker", "work_revision": 1, "control_file": submitted.ControlFile, "owner_capability": local.OwnerCapability, "host_sha256": hex.EncodeToString(digest[:])})
	recovered := origin.call(t, []string{"recover-host", "--state-dir", state, "--request", recovery})
	if recovered.ExitCode == 0 || !bytes.Contains(recovered.Stderr, []byte(`"error":"host_recovery_unavailable"`)) {
		t.Fatalf("Coordinator-loss UNKNOWN recovery was accepted: exit=%d stdout=%s stderr=%s", recovered.ExitCode, recovered.Stdout, recovered.Stderr)
	}
	after := snapshot()
	if after != before {
		t.Fatalf("recovery rejection mutated UNKNOWN facts: before=%s after=%s", before, after)
	}
	if err = second.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal("signal restarted Coordinator", err)
	}
	select {
	case waitErr := <-second.done:
		secondWaited = true
		if waitErr != nil || second.cmd.ProcessState == nil || second.cmd.ProcessState.ExitCode() != 0 {
			t.Fatal("restarted Coordinator graceful original Cmd.Wait", waitErr, second.cmd.ProcessState)
		}
	case <-time.After(13 * time.Second):
		t.Fatal("restarted Coordinator did not gracefully join")
	}
	origin.close(t)
	t.Logf("Coordinator loss retained signed started_wait_owned UNKNOWN: epoch %d->%d; first PID=%d SIGKILL original Wait; FirstHost original Wait=%+v; %s", firstEpoch, secondEpoch, first.cmd.Process.Pid, hostWait, after)
	clean = true
}
