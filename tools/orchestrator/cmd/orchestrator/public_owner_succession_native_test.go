package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type publicSuccessionWait struct {
	Kind      string `json:"kind"`
	PID       int    `json:"pid"`
	ExitCode  int    `json:"exit_code"`
	WaitError string `json:"wait_error"`
}

// Only the task-private test binary dispatches these fixture roles. Public
// actions and original-owner use the unchanged production run entrypoint.
func runPublicOwnerSuccessionFixture() {
	var err error
	switch os.Args[1] {
	case "source-host":
		err = publicSuccessionOwnedHostLauncher(os.Args[2:])
	case "public-succession-owned-host":
		err = publicSuccessionOwnedHost(os.Args[2:])
	case "original-owner":
		if trigger := os.Getenv("PUBLIC_SUCCESSION_BOUND_ABANDONMENT"); trigger != "" && fileExists(trigger) {
			err = publicSuccessionBoundAbandonmentOriginalOwner(os.Args[2:])
		} else {
			err = run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)
		}
	default:
		err = run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)
	}
	if err != nil {
		if os.Args[1] == "original-owner" {
			// The production child intentionally has no stdout/stderr. Capture
			// its actual returned error only in this task-private executable.
			raw, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "error": err.Error(), "code": errorCode(err)})
			_ = os.WriteFile(filepath.Join(filepath.Dir(os.Args[0]), fmt.Sprintf("original-owner-error-%d.json", os.Getpid())), append(raw, '\n'), 0600)
		}
		_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"version": 1, "status": "error", "error": errorCode(err)})
		os.Exit(2)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// publicSuccessionBoundAbandonmentOriginalOwner is a test-binary-only role.
// Its trigger is created immediately before the inc2 public Resume and removed
// after that role has durably Bound, so inc1 and inc3 execute originalOwnerEntry.
func publicSuccessionBoundAbandonmentOriginalOwner(args []string) error {
	trigger := os.Getenv("PUBLIC_SUCCESSION_BOUND_ABANDONMENT")
	if _, err := os.Stat(trigger); err != nil {
		return fmt.Errorf("bound-abandonment trigger: %w", err)
	}
	fs := flag.NewFlagSet("fixture-bound-abandonment-owner", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	bootstrapFD := fs.Int("bootstrap-fd", -1, "")
	controlFD := fs.Int("control-fd", -1, "")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *bootstrapFD != 3 || *controlFD != 4 {
		return errors.New("fixture bound-abandonment owner args")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	control, err := ipc.TakeOwnerControlFD(*controlFD)
	if err != nil {
		return err
	}
	defer control.Close()
	raw, err := ipc.ReadOwnerBootstrapPipe(ctx, *bootstrapFD)
	if err != nil {
		return err
	}
	bootstrap, err := contract.DecodeOwnerRuntimeBootstrap(raw)
	if err != nil {
		return err
	}
	if bootstrap.Prepared.Intent.Scope.Incarnation != 2 {
		return fmt.Errorf("bound-abandonment expected inc2, got %d", bootstrap.Prepared.Intent.Scope.Incarnation)
	}
	birth, err := process.BirthContext(ctx, os.Getpid())
	if err != nil {
		return err
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	coordinatorKey, err := hex.DecodeString(bootstrap.Prepared.CoordinatorPublicKey)
	if err != nil {
		return err
	}
	owner := contract.OriginalOwnerIdentity{PID: os.Getpid(), Birth: birth, PublicKey: hex.EncodeToString(public)}
	client := ipc.OwnerClient{SocketPath: bootstrap.Prepared.RPCSocketPath, Intent: bootstrap.Prepared.Intent, Owner: owner, PrivateKey: private, CoordinatorKey: ed25519.PublicKey(coordinatorKey)}
	intent := bootstrap.Prepared.Intent
	reply, err := client.Call(ctx, 0, "bind", intent.RequestID, contract.OriginalOwnerHello{Scope: intent.Scope, BootstrapID: intent.BootstrapID, BootstrapToken: bootstrap.BootstrapToken, Owner: owner, FirstHost: intent.FirstHost, CoordinatorKeyID: intent.CoordinatorKeyID})
	if err != nil {
		return err
	}
	var wire struct {
		Binding json.RawMessage `json:"binding"`
		Permit  json.RawMessage `json:"permit"`
	}
	if err := contract.DecodeOwnerRuntimeStrict(reply.Body, &wire); err != nil {
		return err
	}
	binding, err := contract.DecodeOriginalOwnerBinding(wire.Binding)
	if err != nil {
		return err
	}
	permit, err := contract.DecodeOwnerControlPermit(wire.Permit)
	if err != nil {
		return err
	}
	body, err := json.Marshal(contract.OwnerBindReply{Binding: binding, Permit: permit})
	if err != nil {
		return err
	}
	if err = control.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	if err = ipc.Write(control, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOriginalOwnerBound, RequestID: intent.RequestID, Epoch: permit.CoordinatorEpoch, Payload: body}); err != nil {
		return err
	}
	return os.WriteFile(trigger+".finished", []byte("bound\n"), 0600)
}

// The controlled launcher retains the registered FirstHost's original Cmd.
// It replaces installation-level launching only; enrollment, owner Start,
// Bind, transport and all worker operations remain production functions.
func publicSuccessionOwnedHostLauncher(args []string) error {
	fs := flag.NewFlagSet("fixture-source-launcher", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	state := fs.String("state-dir", "", "")
	fd := fs.Int("bootstrap-fd", -1, "")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *fd != 3 {
		return errors.New("fixture source launcher args")
	}
	bootstrap, err := readBootstrapFD(*fd)
	if err != nil || !validHostBootstrap(*state, bootstrap) {
		return errors.Join(errors.New("fixture source launcher bootstrap"), err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer reader.Close()
	defer writer.Close()
	reports, childReports, err := os.Pipe()
	if err != nil {
		return err
	}
	defer reports.Close()
	defer childReports.Close()
	cmd := exec.Command(os.Args[0], "public-succession-owned-host", "--state-dir", *state, "--bootstrap-fd", "3", "--wait-report-fd", "4")
	cmd.ExtraFiles = []*os.File{reader, childReports}
	cmd.Env = sourceEnvironment()
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = reader.Close()
	_ = childReports.Close()
	ownedDone := make(chan error, 1)
	go func() { ownedDone <- cmd.Wait() }()
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-ownedDone:
			case <-time.After(13 * time.Second):
				_ = cmd.Process.Kill()
				<-ownedDone
				fmt.Fprintln(os.Stderr, "fixture forced FirstHost cleanup")
			}
		}
	}()
	if err := writer.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	if err := json.NewEncoder(writer).Encode(bootstrap); err != nil {
		return err
	}
	_ = writer.Close()
	if err := reports.SetReadDeadline(time.Now().Add(90 * time.Second)); err != nil {
		return err
	}
	decoder := json.NewDecoder(reports)
	for {
		var observed publicSuccessionWait
		if err := decoder.Decode(&observed); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return err
		}
		if observed.Kind != "owner" || observed.PID <= 1 {
			return errors.New("fixture invalid Owner Wait report")
		}
		if err := publicSuccessionSaveWait(*state, observed); err != nil {
			return err
		}
	}
	var waitErr error
	select {
	case waitErr = <-ownedDone:
		waited = true
	case <-time.After(13 * time.Second):
		return errors.New("fixture original FirstHost Wait timeout")
	}
	if cmd.ProcessState == nil {
		return errors.New("fixture original FirstHost ProcessState absent")
	}
	observed := publicSuccessionWait{Kind: "first_host", PID: cmd.Process.Pid, ExitCode: cmd.ProcessState.ExitCode()}
	if waitErr != nil {
		observed.WaitError = waitErr.Error()
	}
	return publicSuccessionSaveWait(*state, observed)
}

func publicSuccessionOwnedHost(args []string) error {
	fs := flag.NewFlagSet("fixture-owned-host", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	state := fs.String("state-dir", "", "")
	fd := fs.Int("bootstrap-fd", -1, "")
	reportFD := fs.Int("wait-report-fd", -1, "")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *fd != 3 || *reportFD != 4 {
		return errors.New("fixture owned Host args")
	}
	bootstrap, err := readBootstrapFD(*fd)
	if err != nil || !validHostBootstrap(*state, bootstrap) {
		return errors.Join(errors.New("fixture owned Host bootstrap"), err)
	}
	if err := syscall.SetNonblock(*reportFD, true); err != nil {
		return err
	}
	syscall.CloseOnExec(*reportFD)
	reports := os.NewFile(uintptr(*reportFD), "fixture-original-wait")
	defer reports.Close()
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		return err
	}
	executable, err := resolvedExecutable()
	if err != nil {
		return err
	}
	bootstrap.Hello.PID, bootstrap.Hello.Birth, bootstrap.Hello.Executable = os.Getpid(), birth, executable
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	var observationErr error
	supervised, runErr := runInitialOriginalOwnerWithWaitObserver(ctx, bootstrap, func(observed initialOwnerWaitObservation) {
		if observed.ProcessState == nil || observed.PID <= 1 {
			observationErr = errors.New("fixture actual Owner Wait has no ProcessState")
			return
		}
		record := publicSuccessionWait{Kind: "owner", PID: observed.PID, ExitCode: observed.ProcessState.ExitCode()}
		if observed.WaitError != nil {
			record.WaitError = observed.WaitError.Error()
		}
		if err := reports.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
			observationErr = err
			return
		}
		observationErr = json.NewEncoder(reports).Encode(record)
	})
	if !supervised {
		return errors.New("fixture received legacy Source mode")
	}
	return errors.Join(runErr, observationErr)
}

func publicSuccessionSaveWait(state string, observed publicSuccessionWait) error {
	dir := filepath.Join(state, "public-succession-waits")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, observed.Kind+"-"+strconv.Itoa(observed.PID)+".json")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(observed)
	return errors.Join(err, f.Close())
}

func TestPublicOwnerSuccessionWaitObserverUsesOriginalHandleOnce(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "public-owner-succession-wait-fixture")
	image, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture, image, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(fixture)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var callbacks atomic.Int32
	observations := make(chan initialOwnerWaitObservation, 2)
	child := &initialOwnerChild{command: cmd, done: make(chan error, 1), observer: func(observed initialOwnerWaitObservation) {
		callbacks.Add(1)
		observations <- observed
	}}
	var callers sync.WaitGroup
	for range 8 {
		callers.Go(func() { child.startWait() })
	}
	callers.Wait()
	select {
	case waitErr := <-child.done:
		var exited *exec.ExitError
		if !errors.As(waitErr, &exited) || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 17 {
			t.Fatal("original actual Cmd Wait did not observe exit17", waitErr, cmd.ProcessState)
		}
		select {
		case observed := <-observations:
			if callbacks.Load() != 1 || observed.PID != cmd.Process.Pid || observed.ProcessState != cmd.ProcessState || observed.WaitError != waitErr {
				t.Fatal("observer did not report the sole original Wait", callbacks.Load(), observed)
			}
		default:
			t.Fatal("original Wait completed without observer")
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-child.done
		t.Fatal("original Cmd Wait timeout")
	}
	t.Logf("original observer child PID=%d Wait=exit status17, actual ProcessState pointer, callbacks=1", cmd.Process.Pid)
}

func TestPublicOwnerSuccessionDeadBothResumesSameAttempt(t *testing.T) {
	testPublicOwnerSuccessionDeadBothResumesSameAttempt(t, false)
}

func TestPublicOwnerSuccessionBoundNeverAppliedResumesAtInc3(t *testing.T) {
	// The fixture intentionally starts several real child processes. Keep the
	// race detector enabled while removing only its process-exit delay.
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	testPublicOwnerSuccessionDeadBothResumesSameAttempt(t, true)
}

func testPublicOwnerSuccessionDeadBothResumesSameAttempt(t *testing.T, abandonBound bool) {
	root, err := os.MkdirTemp("/private/tmp", "public-owner-succession-")
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
			t.Log("retained private public succession fixture:", root)
		}
	})
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("controlled CLI/worker binary sha256=%x", sha256.Sum256(image))
	cli, worker := filepath.Join(root, "public-owner-succession-cli-fixture"), filepath.Join(root, "budget-revision-worker-fixture")
	for _, path := range []string{cli, worker} {
		if err := os.WriteFile(path, image, 0700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	logPath := filepath.Join(root, "serve.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	server := exec.Command(cli, "serve", "--state-dir", state)
	server.Env = append(os.Environ(), "ORCHESTRATOR_ENABLE_TEST_FAKE=1")
	boundAbandonment := filepath.Join(root, "bound-abandonment")
	if abandonBound {
		t.Setenv("PUBLIC_SUCCESSION_BOUND_ABANDONMENT", boundAbandonment)
		server.Env = append(server.Env, "PUBLIC_SUCCESSION_BOUND_ABANDONMENT="+boundAbandonment)
	}
	server.Stdout, server.Stderr = log, log
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Wait() }()
	marker, release := filepath.Join(root, "worker.json"), filepath.Join(root, "release")
	var actors []contract.OriginalOwnerTransport
	t.Cleanup(func() {
		dbURL := url.URL{Scheme: "file", Path: filepath.Join(state, "state.db"), RawQuery: "mode=ro"}
		if db, err := sql.Open("sqlite", dbURL.String()); err == nil {
			rows, err := db.Query(`SELECT pid,birth FROM runtime_hosts WHERE launch_id IN (SELECT launch_id FROM execution_owners WHERE run_id='public-succession-run') UNION SELECT owner_pid,owner_birth FROM execution_owners WHERE run_id='public-succession-run' AND owner_pid IS NOT NULL`)
			if err == nil {
				for rows.Next() {
					var actor contract.OriginalOwnerTransport
					if rows.Scan(&actor.PID, &actor.Birth) == nil && actor.Validate() == nil {
						actors = append(actors, actor)
					}
				}
				_ = rows.Close()
			}
			_ = db.Close()
		}
		_ = os.WriteFile(release, nil, 0600)
		_ = os.WriteFile(release+".retry", nil, 0600)
		_ = server.Process.Signal(syscall.SIGTERM)
		select {
		case waitErr := <-serverDone:
			t.Logf("Coordinator original Cmd PID=%d Wait=%v state=%v", server.Process.Pid, waitErr, server.ProcessState)
			if waitErr != nil {
				t.Error("Coordinator graceful original Wait", waitErr)
			}
		case <-time.After(13 * time.Second):
			_ = server.Process.Kill()
			<-serverDone
			t.Error("forced Coordinator cleanup")
		}
		_ = log.Close()
		for _, actor := range actors {
			for end := time.Now().Add(13 * time.Second); ; {
				gone, err := process.IdentityExitedContext(context.Background(), actor.PID, actor.Birth)
				if err == nil && gone {
					break
				}
				if time.Now().After(end) {
					if actual, err := process.Birth(actor.PID); err == nil && actual == actor.Birth {
						_ = syscall.Kill(actor.PID, syscall.SIGTERM)
					}
					t.Errorf("actor did not retire naturally PID=%d birth=%q err=%v", actor.PID, actor.Birth, err)
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		for _, actor := range actors {
			for _, kind := range []string{"owner", "first_host"} {
				path := filepath.Join(state, "public-succession-waits", fmt.Sprintf("%s-%d.json", kind, actor.PID))
				if raw, err := os.ReadFile(path); err == nil {
					t.Logf("original Cmd Wait record %s: %s", filepath.Base(path), raw)
				}
			}
		}
		if paths, err := filepath.Glob(filepath.Join(root, "original-owner-error-*.json")); err == nil {
			for _, path := range paths {
				if raw, err := os.ReadFile(path); err == nil {
					t.Logf("actual Owner run error %s: %s", filepath.Base(path), raw)
				}
			}
		}
		for _, name := range []string{"source-host.log", "coordinator.log"} {
			if raw, err := os.ReadFile(filepath.Join(state, name)); err == nil {
				t.Logf("raw %s:\n%s", name, raw)
			}
		}
	})
	// A cold race-instrumented SQLite migration can consume the previous
	// five-second fixture setup window. This is separate from Owner startup
	// and its unchanged thirteen-second original-Wait retirement contract.
	readyStarted := time.Now()
	for end := readyStarted.Add(15 * time.Second); ; {
		readyCtx, stopReady := context.WithTimeout(ctx, 150*time.Millisecond)
		response, readyErr := ipc.Call(readyCtx, filepath.Join(state, "coordinator.sock"), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindReady, RequestID: "public-succession-ready"})
		stopReady()
		if readyErr == nil && response.Kind == ipc.KindResponse {
			t.Logf("owned Coordinator ready after %s", time.Since(readyStarted))
			break
		}
		if time.Now().After(end) {
			t.Fatal("owned Coordinator did not become ready", readyErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"run_id": "public-succession-run", "controller_thread": "public-succession-controller", "plan_revision": 1,
		"origin_context_id": "public-succession-origin", "origin_pid": os.Getpid(), "origin_birth": birth, "host_generation": "public-succession-generation", "delivery_mode": "collect",
		"tasks": []any{map[string]any{"id": "worker", "max_attempts": 1, "max_active_ms": 30000, "completion_policy": "owner_review",
			"execution_policy": contract.ExecutionPolicy{Version: 1, GroupMaxAttempts: 1, GroupMaxActiveMS: 60000, TaskMaxActiveMS: 60000, SegmentMaxActiveMS: 30000},
			"adapter":          map[string]any{"kind": "fake", "args": []string{worker, marker, release}, "directory": root}}}}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(root, "submit.json")
	if err := os.WriteFile(requestPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var submitted struct {
		ControlFile string `json:"control_file"`
	}
	if err := json.Unmarshal(runBinary(t, ctx, cli, "submit", "--state-dir", state, "--request", requestPath), &submitted); err != nil {
		t.Fatal(err)
	}
	capability, err := loadControlCapability(submitted.ControlFile)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := loadHostBootstrap(capability)
	if err != nil {
		t.Fatal(err)
	}
	dbURL := url.URL{Scheme: "file", Path: filepath.Join(state, "state.db"), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", dbURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	oldBinding := publicSuccessionBinding(t, db)
	if oldBinding.Scope.Incarnation != 1 || oldBinding.Scope.OriginalOriginPID != os.Getpid() || oldBinding.Scope.OriginalOriginBirth != birth {
		t.Fatal("initial public binding", oldBinding.Scope)
	}
	actors = append(actors, oldBinding.FirstHost, contract.OriginalOwnerTransport{PID: oldBinding.Owner.PID, Birth: oldBinding.Owner.Birth})
	firstWorker := publicSuccessionWorker(t, marker)
	actors = append(actors, contract.OriginalOwnerTransport{PID: firstWorker.PID, Birth: firstWorker.Birth})
	publicSuccessionAncestry(t, ctx, firstWorker, oldBinding)
	before := publicSuccessionStatus(t, ctx, cli, state, submitted.ControlFile, "running", "running")
	runBinary(t, ctx, cli, "stop", "--state-dir", state, "--task-id", "worker", "--control-file", submitted.ControlFile, "--work-revision", "1")
	stopped := publicSuccessionStatus(t, ctx, cli, state, submitted.ControlFile, "interrupted", "exited")
	if stopped.AttemptID != before.AttemptID || stopped.SegmentID != before.SegmentID {
		t.Fatal("Stop replaced original attempt/segment")
	}
	publicSuccessionExit(t, bootstrap, before.AttemptID, before.SegmentID, firstWorker, false)
	var used, grant, deadline int64
	if err := db.QueryRow(`SELECT b.used_ms,b.granted_ms,s.deadline_unix_ms FROM budget_runtime b JOIN segment_runtime s ON s.segment_id=b.segment_id WHERE b.segment_id=? AND b.status='settled'`, before.SegmentID).Scan(&used, &grant, &deadline); err != nil || used <= 0 || grant != 30000 {
		t.Fatal("original settled charge/grant", used, grant, err)
	}
	ledgerPath := filepath.Join(bootstrap.SpoolRoot, ".coordinator-recovery.json")
	priorLedger, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if actual, err := process.Birth(oldBinding.Owner.PID); err != nil || actual != oldBinding.Owner.Birth {
		t.Fatal("old Owner identity changed before Signal", err)
	}
	if err := syscall.Kill(oldBinding.Owner.PID, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	ownerWait := publicSuccessionReadWait(t, state, "owner", oldBinding.Owner.PID)
	hostWait := publicSuccessionReadWait(t, state, "first_host", oldBinding.FirstHost.PID)
	if ownerWait.ExitCode != 0 || ownerWait.WaitError != "" || hostWait.ExitCode != 0 || hostWait.WaitError != "" {
		t.Fatal("old Owner did not return graceful original Wait", ownerWait)
	}
	t.Logf("old Owner original Wait=%+v; old FirstHost original Wait=%+v", ownerWait, hostWait)
	for _, actor := range actors[:2] {
		gone, err := process.IdentityExitedContext(ctx, actor.PID, actor.Birth)
		if err != nil || !gone {
			t.Fatal("original tuple lacks affirmative exit proof", actor, gone, err)
		}
	}
	if current, err := process.Birth(os.Getpid()); err != nil || current != birth {
		t.Fatal("original origin changed before Resume", err)
	}
	const commandID = "public-dead-both-successor"
	if abandonBound {
		if err := os.WriteFile(boundAbandonment, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	resumeArgs := []string{"resume", "--state-dir", state, "--task-id", "worker", "--control-file", submitted.ControlFile, "--work-revision", "1", "--command-id", commandID}
	var resumeBody []byte
	if abandonBound {
		command := exec.CommandContext(ctx, cli, resumeArgs...)
		command.Env = append(os.Environ(), "ORCHESTRATOR_ENABLE_TEST_FAKE=1")
		var resumeErr error
		resumeBody, resumeErr = command.CombinedOutput()
		if resumeErr == nil || !bytes.Contains(resumeBody, []byte(`"error":"owner_resume_pending_blocked"`)) {
			t.Fatal("first public Resume did not expose the bound-never-Applied pending state", resumeErr, string(resumeBody))
		}
	} else {
		resumeBody = runBinary(t, ctx, cli, resumeArgs...)
	}
	var receipt struct {
		Mode, CommandID string
		ReadySequence   int64 `json:"ready_sequence"`
	}
	var receiptFields map[string]json.RawMessage
	if !abandonBound {
		if err := json.Unmarshal(resumeBody, &receiptFields); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(receiptFields["mode"], &receipt.Mode)
		_ = json.Unmarshal(receiptFields["command_id"], &receipt.CommandID)
		_ = json.Unmarshal(receiptFields["ready_sequence"], &receipt.ReadySequence)
		if receipt.Mode != "successor" || receipt.CommandID != commandID {
			t.Fatal("public Resume did not return exact successor receipt", string(resumeBody))
		}
	}
	newBinding := publicSuccessionBinding(t, db)
	if newBinding.Scope.Incarnation != 2 || newBinding.Scope.RunID != oldBinding.Scope.RunID || newBinding.Scope.LaunchID != oldBinding.Scope.LaunchID || newBinding.Owner == oldBinding.Owner || newBinding.FirstHost == oldBinding.FirstHost {
		t.Fatal("public Resume did not create adjacent real successor", newBinding)
	}
	if abandonBound {
		for end := time.Now().Add(8 * time.Second); ; {
			if raw, err := os.ReadFile(boundAbandonment + ".finished"); err == nil && bytes.Equal(raw, []byte("bound\n")) {
				break
			}
			if time.Now().After(end) {
				t.Fatal("bound-never-Applied test owner did not finish signed Bind")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err := os.Remove(boundAbandonment); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT ready_sequence FROM owner_succession_authorizations WHERE command_id=?`, commandID).Scan(&receipt.ReadySequence); err != nil || receipt.ReadySequence <= 0 {
			t.Fatal("bound-never-Applied first Resume lacks durable ready sequence", receipt.ReadySequence, err)
		}
		boundOwnerWait := publicSuccessionReadWait(t, state, "owner", newBinding.Owner.PID)
		boundHostWait := publicSuccessionReadWait(t, state, "first_host", newBinding.FirstHost.PID)
		if boundOwnerWait.ExitCode != 0 || boundOwnerWait.WaitError != "" {
			t.Fatal("bound-never-Applied Owner original Wait failed", boundOwnerWait)
		}
		if boundHostWait.ExitCode != 0 || boundHostWait.WaitError != "" {
			t.Fatal("bound-never-Applied FirstHost original Wait changed", boundHostWait)
		}
		t.Logf("bound-never-Applied inc2 Owner original Wait=%+v; FirstHost original Wait=%+v", boundOwnerWait, boundHostWait)
		secondCommandID := "public-bound-abandonment-successor"
		secondBody := runBinary(t, ctx, cli, "resume", "--state-dir", state, "--task-id", "worker", "--control-file", submitted.ControlFile, "--work-revision", "1", "--command-id", secondCommandID)
		var secondReceipt struct {
			Mode          string `json:"mode"`
			CommandID     string `json:"command_id"`
			ReadySequence int64  `json:"ready_sequence"`
		}
		if err := json.Unmarshal(secondBody, &secondReceipt); err != nil || secondReceipt.Mode != "successor" || secondReceipt.CommandID != secondCommandID || secondReceipt.ReadySequence != receipt.ReadySequence {
			t.Fatal("second public Resume did not return successor receipt", string(secondBody), err)
		}
		t.Logf("bound-never-Applied inc2->inc3 preserved ready sequence=%d", receipt.ReadySequence)
		thirdBinding := publicSuccessionBinding(t, db)
		if thirdBinding.Scope.Incarnation != 3 || thirdBinding.Scope.RunID != newBinding.Scope.RunID || thirdBinding.Scope.LaunchID != newBinding.Scope.LaunchID {
			t.Fatal("bound-never-Applied resume did not create inc3", thirdBinding)
		}
		newBinding = thirdBinding
	}
	actors = append(actors, newBinding.FirstHost, contract.OriginalOwnerTransport{PID: newBinding.Owner.PID, Birth: newBinding.Owner.Birth})
	secondWorker := publicSuccessionWorker(t, marker+".retry")
	actors = append(actors, contract.OriginalOwnerTransport{PID: secondWorker.PID, Birth: secondWorker.Birth})
	publicSuccessionAncestry(t, ctx, secondWorker, newBinding)
	after := publicSuccessionStatus(t, ctx, cli, state, submitted.ControlFile, "running", "running")
	if after.AttemptID != before.AttemptID || after.SegmentID == before.SegmentID || after.WorkRevision != before.WorkRevision {
		t.Fatal("successor reset attempt/revision or reused segment", before, after)
	}
	var oldUsed, oldGrant, oldDeadline, newGrant int64
	if err := db.QueryRow(`SELECT b.used_ms,b.granted_ms,s.deadline_unix_ms,(SELECT granted_ms FROM budget_runtime WHERE segment_id=?) FROM budget_runtime b JOIN segment_runtime s ON s.segment_id=b.segment_id WHERE b.segment_id=?`, after.SegmentID, before.SegmentID).Scan(&oldUsed, &oldGrant, &oldDeadline, &newGrant); err != nil || oldUsed != used || oldGrant != grant || oldDeadline != deadline || newGrant != grant-used {
		t.Fatal("successor changed budget history or remaining grant", oldUsed, oldGrant, oldDeadline, newGrant, err)
	}
	var attempts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM attempts WHERE task_id='worker'`).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatal("Resume started another attempt", attempts, err)
	}
	currentLedger, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	var prior, current map[string]json.RawMessage
	if json.Unmarshal(priorLedger, &prior) != nil || json.Unmarshal(currentLedger, &current) != nil {
		t.Fatal("invalid current/prior ledger")
	}
	scope, _ := contract.OriginalOwnerRecoveryScopeSHA256(newBinding.Scope, newBinding.Owner)
	wantScope, _ := json.Marshal(scope)
	if !bytes.Equal(current["scope_sha256"], wantScope) || bytes.Equal(prior["scope_sha256"], current["scope_sha256"]) {
		t.Fatal("scope handoff not bound to exact adjacent owner")
	}
	prior["scope_sha256"] = current["scope_sha256"]
	a, _ := json.Marshal(prior)
	b, _ := json.Marshal(current)
	if !bytes.Equal(a, b) {
		t.Fatal("successor changed non-scope recovery fields")
	}
	if !abandonBound {
		if replay := runBinary(t, ctx, cli, resumeArgs...); !bytes.Equal(replay, resumeBody) {
			t.Fatal("stable command did not replay immutable receipt", string(replay))
		}
	}
	if got := publicSuccessionWorker(t, marker+".retry"); got != secondWorker {
		t.Fatal("Resume replay replaced worker")
	}
	if err := os.WriteFile(release+".retry", nil, 0600); err != nil {
		t.Fatal(err)
	}
	terminal := publicSuccessionStatus(t, ctx, cli, state, submitted.ControlFile, "result_ready", "exited")
	publicSuccessionExit(t, bootstrap, terminal.AttemptID, terminal.SegmentID, secondWorker, true)
	var collection struct {
		Events []contract.Event `json:"events"`
	}
	if err := json.Unmarshal(runBinary(t, ctx, cli, "collect", "--state-dir", state, "--task-id", "worker", "--control-file", submitted.ControlFile), &collection); err != nil {
		t.Fatal(err)
	}
	var result contract.Event
	for _, event := range collection.Events {
		if event.Kind == contract.EventResult && event.SegmentID == after.SegmentID {
			if result.EventID != "" {
				t.Fatal("multiple successor results")
			}
			result = event
		}
	}
	if result.EventID == "" || result.ActionSlot == "" {
		t.Fatal("public collect lacks actionable result")
	}
	runBinary(t, ctx, cli, "accept", "--state-dir", state, "--task-id", "worker", "--work-revision", "1", "--control-file", submitted.ControlFile,
		"--event-id", result.EventID, "--event-revision", strconv.FormatInt(result.EventRevision, 10), "--event-hash", result.PayloadHash, "--action-slot", result.ActionSlot, "--decision", "accept", "--command-id", "public-succession-accept")
	publicSuccessionStatus(t, ctx, cli, state, submitted.ControlFile, "completed", "exited")
	// Completed work triggers authenticated host_retired while Coordinator is
	// still alive. Observe its natural original Wait, without racing a signal
	// against a process that may already have retired successfully.
	newOwnerWait := publicSuccessionReadWait(t, state, "owner", newBinding.Owner.PID)
	newHostWait := publicSuccessionReadWait(t, state, "first_host", newBinding.FirstHost.PID)
	if newOwnerWait.ExitCode != 0 || newOwnerWait.WaitError != "" || newHostWait.ExitCode != 0 || newHostWait.WaitError != "" {
		t.Fatal("successor runtime graceful original Wait failed", newOwnerWait, newHostWait)
	}
	for _, pid := range []int{newBinding.Owner.PID, newBinding.FirstHost.PID} {
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatal("retired successor original Wait did not reap process", pid, err)
		}
	}
	t.Logf("successor Owner original Wait=%+v; successor FirstHost original Wait=%+v", newOwnerWait, newHostWait)
	t.Logf("public stable Resume/collect/accept completed inc%d same attempt=%s new segment=%s; retained charge=%d grant=%d next grant=%d", newBinding.Scope.Incarnation, after.AttemptID, after.SegmentID, used, grant, newGrant)
	clean = true
}

func publicSuccessionBinding(t *testing.T, db *sql.DB) contract.OriginalOwnerBinding {
	t.Helper()
	var raw string
	if err := db.QueryRow(`SELECT original_binding_json FROM execution_owners WHERE run_id='public-succession-run' AND is_current=1 AND phase='bound'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var binding contract.OriginalOwnerBinding
	if err := json.Unmarshal([]byte(raw), &binding); err != nil {
		t.Fatal(err)
	}
	return binding
}

func publicSuccessionWorker(t *testing.T, marker string) originalOwnerWorkerMarker {
	t.Helper()
	for end := time.Now().Add(8 * time.Second); ; {
		raw, err := os.ReadFile(marker)
		var observed originalOwnerWorkerMarker
		if err == nil && json.Unmarshal(raw, &observed) == nil && observed.PID > 1 && observed.Birth != "" {
			return observed
		}
		if time.Now().After(end) {
			t.Fatal("worker marker missing", marker, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func publicSuccessionStatus(t *testing.T, ctx context.Context, cli, state, control, status, segment string) recoveryStatus {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); ; {
		observed := recoveryTaskStatus(t, ctx, cli, state, "worker", control)
		if observed.Status == status && observed.SegmentStatus == segment {
			return observed
		}
		if time.Now().After(end) || ctx.Err() != nil {
			t.Fatal("public state did not converge", status, segment, observed, ctx.Err())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func publicSuccessionAncestry(t *testing.T, ctx context.Context, worker originalOwnerWorkerMarker, binding contract.OriginalOwnerBinding) {
	t.Helper()
	chain, err := process.AncestryContext(ctx, worker.PID)
	if err != nil || worker.ParentPID != binding.Owner.PID || chain[worker.PID] != worker.Birth || chain[binding.Owner.PID] != binding.Owner.Birth || chain[binding.FirstHost.PID] != binding.FirstHost.Birth {
		t.Fatal("worker ancestry not exact Owner/FirstHost", worker, binding.Owner, err)
	}
}

func publicSuccessionReadWait(t *testing.T, state, kind string, pid int) publicSuccessionWait {
	t.Helper()
	path := filepath.Join(state, "public-succession-waits", kind+"-"+strconv.Itoa(pid)+".json")
	for end := time.Now().Add(18 * time.Second); ; {
		raw, err := os.ReadFile(path)
		var observed publicSuccessionWait
		if err == nil && json.Unmarshal(raw, &observed) == nil && observed.Kind == kind && observed.PID == pid {
			return observed
		}
		if time.Now().After(end) {
			t.Fatal("original Wait report absent", kind, pid, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func publicSuccessionExit(t *testing.T, bootstrap hostBootstrap, attempt, segment string, worker originalOwnerWorkerMarker, success bool) {
	t.Helper()
	spool, err := events.Open(filepath.Join(bootstrap.SpoolRoot, attempt, segment))
	if err != nil {
		t.Fatal(err)
	}
	observed, err := spool.Read()
	if err != nil {
		t.Fatal(err)
	}
	exits, stops := 0, 0
	for _, event := range observed {
		if event.Kind == contract.EventStopped && event.StopReason == "owner_stop_requested" {
			stops++
		}
		if event.Kind == contract.EventExited {
			exits++
			if event.ExitCode == nil || (*event.ExitCode == 0) != success || event.Process == nil || event.Process.PID != worker.PID || event.Process.Birth != worker.Birth {
				t.Fatal("original worker Exited receipt mismatch", event)
			}
			t.Logf("actual worker original Exited: segment=%s process=%+v exit=%d", segment, event.Process, *event.ExitCode)
		}
	}
	if exits != 1 || (!success && stops != 1) {
		t.Fatal("original exit/stop count", exits, stops)
	}
	if !errors.Is(syscall.Kill(worker.PID, 0), syscall.ESRCH) {
		t.Fatal("worker original Wait did not reap exact process", worker.PID)
	}
}
