package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type originalOwnerWorkerMarker struct {
	PID, ParentPID int
	Birth          string
}

func runOriginalOwnerWorkerFixture() {
	if len(os.Args) != 3 {
		os.Exit(21)
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		os.Exit(22)
	}
	raw, _ := json.Marshal(originalOwnerWorkerMarker{os.Getpid(), os.Getppid(), birth})
	if err = os.WriteFile(os.Args[1]+".tmp", raw, 0600); err != nil {
		os.Exit(23)
	}
	if err = os.Rename(os.Args[1]+".tmp", os.Args[1]); err != nil {
		os.Exit(24)
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(os.Args[2]); err == nil {
			fmt.Println("original owner worker result")
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
	os.Exit(25)
}

// This is the actual public submit/Source Host/original-owner chain. Only the
// task adapter is a controlled worker; enrollment, signatures, process handles,
// Store, spool publication and Coordinator shutdown are production paths.
func TestCLIOriginalOwnerExecutesSignedWorker(t *testing.T) {
	exerciseCLIOriginalOwner(t, false)
}

func TestCLIOriginalOwnerReplacesDeadTransport(t *testing.T) {
	exerciseCLIOriginalOwner(t, true)
}

func exerciseCLIOriginalOwner(t *testing.T, killTransport bool) {
	t.Helper()
	root, err := os.MkdirTemp("/private/tmp", "owner-first-cli-")
	if err != nil {
		t.Fatal(err)
	}
	clean := false
	t.Cleanup(func() {
		if clean && !t.Failed() {
			_ = os.RemoveAll(root)
		} else {
			t.Logf("retained original-owner CLI runtime: %s", root)
		}
	})
	bin, state := filepath.Join(root, "orchestrator"), filepath.Join(root, "state")
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), time.Minute)
	out, err := exec.CommandContext(buildCtx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", bin, ".").CombinedOutput()
	cancelBuild()
	if err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(root, "original-owner-worker-fixture")
	image, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(fixture, image, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	log, err := os.OpenFile(filepath.Join(root, "serve.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	server := exec.Command(bin, "serve", "--state-dir", state)
	server.Env = append(os.Environ(), "ORCHESTRATOR_ENABLE_TEST_FAKE=1")
	server.Stdout, server.Stderr = log, log
	if err = server.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- server.Wait() }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = server.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-wait:
			if err != nil {
				t.Errorf("Coordinator original Wait: %v", err)
			} else {
				t.Log("Coordinator original Wait=0")
			}
		case <-time.After(13 * time.Second):
			_ = server.Process.Kill()
			select {
			case <-wait:
			case <-time.After(time.Second):
				t.Error("Coordinator original Wait unavailable after owned Kill")
			}
			t.Error("Coordinator required forced cleanup")
		}
		_ = log.Close()
	}
	markerPath, releasePath := filepath.Join(root, "worker.json"), filepath.Join(root, "release")
	var actors []contract.OriginalOwnerTransport
	t.Cleanup(func() {
		// Submit can start a transport and then fail before returning its
		// control path. Capture only this private test database's identities
		// before shutdown, including that incomplete-enrollment failure path.
		u := url.URL{Scheme: "file", Path: filepath.Join(state, "state.db"), RawQuery: "mode=ro"}
		if db, err := sql.Open("sqlite", u.String()); err == nil {
			readCtx, stopRead := context.WithTimeout(context.Background(), 2*time.Second)
			rows, err := db.QueryContext(readCtx, `SELECT h.pid,h.birth FROM runtime_hosts h WHERE EXISTS (SELECT 1 FROM task_runtime tr JOIN tasks t ON t.id=tr.task_id WHERE tr.host_launch_id=h.launch_id AND t.run_id='owner-cli-run') UNION SELECT owner_pid,owner_birth FROM execution_owners WHERE run_id='owner-cli-run' AND owner_pid IS NOT NULL`)
			if err == nil {
				for rows.Next() {
					var actor contract.OriginalOwnerTransport
					if err := rows.Scan(&actor.PID, &actor.Birth); err == nil && actor.Validate() == nil {
						actors = append(actors, actor)
					}
				}
				_ = rows.Close()
			}
			stopRead()
			_ = db.Close()
		}
		if raw, err := os.ReadFile(markerPath); err == nil {
			var marker originalOwnerWorkerMarker
			if json.Unmarshal(raw, &marker) == nil && marker.PID > 0 && marker.Birth != "" {
				actors = append(actors, contract.OriginalOwnerTransport{PID: marker.PID, Birth: marker.Birth})
			}
		}
		_ = os.WriteFile(releasePath, nil, 0600)
		stop()
		owned := func(actor contract.OriginalOwnerTransport) bool {
			live, err := process.Birth(actor.PID)
			if err != nil || live != actor.Birth {
				return false
			}
			executable, err := process.ExecutablePath(actor.PID)
			return err == nil && (executable == bin || executable == fixture)
		}
		// Signal only identities observed in this task's authenticated chain.
		// Normal shutdown should already have retired all of them.
		for _, actor := range actors {
			if owned(actor) {
				_ = syscall.Kill(actor.PID, syscall.SIGTERM)
			}
		}
		end := time.Now().Add(13 * time.Second)
		for _, actor := range actors {
			for {
				if !owned(actor) {
					break
				}
				if time.Now().After(end) {
					_ = syscall.Kill(actor.PID, syscall.SIGKILL)
					t.Errorf("owned actor %d required forced cleanup; not a Wait receipt", actor.PID)
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
	})
	for deadline := time.Now().Add(5 * time.Second); ; {
		readyCtx, stopReady := context.WithTimeout(ctx, 150*time.Millisecond)
		ready, readyErr := ipc.Call(readyCtx, filepath.Join(state, "coordinator.sock"), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindReady, RequestID: "owner-cli-ready"})
		stopReady()
		if readyErr == nil && ready.Kind == ipc.KindResponse {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Coordinator not ready: %v", readyErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	request := map[string]any{
		"run_id": "owner-cli-run", "controller_thread": "owner-cli-thread", "plan_revision": 1,
		"origin_context_id": "owner-cli-origin", "origin_pid": os.Getpid(), "origin_birth": birth, "host_generation": "owner-cli-generation",
		"delivery_mode": "collect",
		"tasks":         []any{map[string]any{"id": "worker", "max_attempts": 1, "max_active_ms": 60000, "completion_policy": "owner_review", "adapter": map[string]any{"kind": "fake", "args": []string{fixture, markerPath, releasePath}, "directory": root}}},
	}
	raw, _ := json.Marshal(request)
	requestPath := filepath.Join(root, "request.json")
	if err := os.WriteFile(requestPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var submitted struct {
		ControlFile string `json:"control_file"`
	}
	if err := json.Unmarshal(runBinary(t, ctx, bin, "submit", "--state-dir", state, "--request", requestPath), &submitted); err != nil {
		t.Fatal(err)
	}
	cap, err := loadControlCapability(submitted.ControlFile)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := loadHostBootstrap(cap)
	if err != nil {
		t.Fatal(err)
	}
	host := coreCLIReadHostIdentity(t, state, cap.HostLaunchID)
	actors = append(actors, contract.OriginalOwnerTransport{PID: host.PID, Birth: host.Birth})
	u := url.URL{Scheme: "file", Path: filepath.Join(state, "state.db"), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var mode, bindingJSON string
	if err = db.QueryRowContext(ctx, `SELECT l.execution_owner_mode,e.original_binding_json FROM host_launches l JOIN execution_owners e ON e.launch_id=l.id WHERE l.id=? AND e.phase='bound' AND e.is_current=1`, cap.HostLaunchID).Scan(&mode, &bindingJSON); err != nil {
		t.Fatal("public CLI did not bind an original owner", err)
	}
	var binding contract.OriginalOwnerBinding
	if err = json.Unmarshal([]byte(bindingJSON), &binding); err != nil || mode != "supervised-v1" || binding.FirstHost.PID != host.PID || binding.FirstHost.Birth != host.Birth || binding.Scope.OriginalOriginPID != os.Getpid() || binding.Scope.OriginalOriginBirth != birth {
		t.Fatal("original owner binding lost initial physical scope", err)
	}
	actors = append(actors, contract.OriginalOwnerTransport{PID: binding.Owner.PID, Birth: binding.Owner.Birth})
	var worker originalOwnerWorkerMarker
	for deadline := time.Now().Add(10 * time.Second); ; {
		raw, err := os.ReadFile(markerPath)
		if err == nil && json.Unmarshal(raw, &worker) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("signed worker did not start", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	actors = append(actors, contract.OriginalOwnerTransport{PID: worker.PID, Birth: worker.Birth})
	chain, err := process.AncestryContext(ctx, worker.PID)
	if err != nil || worker.ParentPID != binding.Owner.PID || worker.ParentPID == host.PID || chain[worker.PID] != worker.Birth || chain[binding.Owner.PID] != binding.Owner.Birth || chain[host.PID] != host.Birth {
		t.Fatalf("worker did not belong to original owner: worker=%+v host=%+v err=%v", worker, host, err)
	}
	if killTransport {
		before := recoveryTaskStatus(t, ctx, bin, state, "worker", submitted.ControlFile)
		if current, err := process.Birth(host.PID); err != nil || current != host.Birth {
			t.Fatal("first transport identity changed before fault", err)
		}
		if err := syscall.Kill(host.PID, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		var recovered recoveryStatus
		for deadline := time.Now().Add(20 * time.Second); ; {
			recovered = recoveryTaskStatus(t, ctx, bin, state, "worker", submitted.ControlFile)
			if recovered.HostPID != host.PID && recovered.HostStatus == "ready" {
				break
			}
			if recovered.Status == "failed" || recovered.Status == "unknown" || recovered.Status == "interrupted" || time.Now().After(deadline) {
				t.Fatalf("live owner did not replace dead first transport: before=%+v after=%+v", before, recovered)
			}
			time.Sleep(20 * time.Millisecond)
		}
		actors = append(actors, contract.OriginalOwnerTransport{PID: recovered.HostPID, Birth: recovered.HostBirth})
		if gone, err := process.IdentityExitedContext(ctx, host.PID, host.Birth); err != nil || !gone {
			t.Fatal("original source transport was not proven exited", gone, err)
		}
		if recovered.AttemptID != before.AttemptID || recovered.SegmentID != before.SegmentID || recovered.WorkRevision != before.WorkRevision {
			t.Fatal("transport recovery redispatched the worker")
		}
		for _, actor := range []contract.OriginalOwnerTransport{{PID: worker.PID, Birth: worker.Birth}, {PID: binding.Owner.PID, Birth: binding.Owner.Birth}, {PID: recovered.HostPID, Birth: recovered.HostBirth}} {
			if current, err := process.Birth(actor.PID); err != nil || current != actor.Birth {
				t.Fatal("recovery lost original owner/worker or replacement identity", err)
			}
		}
		parents, err := process.AncestryContext(ctx, recovered.HostPID)
		if err != nil || parents[binding.Owner.PID] != binding.Owner.Birth {
			t.Fatal("replacement transport is not a child of original owner", err)
		}
		parentRaw, err := exec.CommandContext(ctx, "/bin/ps", "-o", "ppid=", "-p", strconv.Itoa(recovered.HostPID)).Output()
		parentPID, parseErr := strconv.Atoi(strings.TrimSpace(string(parentRaw)))
		if err != nil || parseErr != nil || parentPID != binding.Owner.PID {
			t.Fatal("replacement transport direct parent is not original owner", parentPID, err, parseErr)
		}
		if birth, err := process.BirthContext(ctx, recovered.HostPID); err != nil || birth != recovered.HostBirth {
			t.Fatal("replacement identity changed during parent observation", err)
		}
		var afterMarker originalOwnerWorkerMarker
		marker, err := os.ReadFile(markerPath)
		if err != nil || json.Unmarshal(marker, &afterMarker) != nil || afterMarker != worker {
			t.Fatal("recovery started a replacement worker")
		}
		var revision int
		var afterBinding string
		if err := db.QueryRowContext(ctx, `SELECT control_revision,original_binding_json FROM execution_owners WHERE run_id='owner-cli-run' AND is_current=1`).Scan(&revision, &afterBinding); err != nil || revision != 2 || afterBinding != bindingJSON {
			t.Fatal("recovery did not retain original owner binding and one control transfer", err)
		}
		t.Log("dead first transport replaced once under original live owner; worker tuple and segment unchanged")
	}
	if err := os.WriteFile(releasePath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var status recoveryStatus
	for {
		status = recoveryTaskStatus(t, ctx, bin, state, "worker", submitted.ControlFile)
		if status.Status == "result_ready" && status.SegmentStatus == "exited" {
			break
		}
		if status.Status == "failed" || status.Status == "unknown" || ctx.Err() != nil {
			t.Fatalf("worker terminal state: %+v", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	spoolPath := filepath.Join(bootstrap.SpoolRoot, status.AttemptID, status.SegmentID)
	if info, err := os.Lstat(spoolPath); err != nil || !info.IsDir() {
		t.Fatal("runtime did not create the worker spool", err)
	}
	spool, err := events.Open(spoolPath)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := spool.Read()
	if err != nil {
		t.Fatal(err)
	}
	exits := 0
	for _, event := range observed {
		if event.Kind == contract.EventUnknown {
			t.Fatal("normal worker became UNKNOWN")
		}
		if event.Kind == contract.EventExited {
			exits++
			if event.ExitCode == nil || *event.ExitCode != 0 || event.Process == nil || event.Process.PID != worker.PID || event.Process.Birth != worker.Birth {
				t.Fatalf("exit receipt lost original worker identity: %+v", event)
			}
		}
	}
	if exits != 1 || !errors.Is(syscall.Kill(worker.PID, 0), syscall.ESRCH) {
		t.Fatal("worker exit was not observed once and reaped")
	}
	t.Log("public supervised CLI worker parent/birth and one durable ExitCode=0 verified")
	stop()
	for _, actor := range actors {
		for end := time.Now().Add(13 * time.Second); ; {
			if errors.Is(syscall.Kill(actor.PID, 0), syscall.ESRCH) {
				break
			}
			if time.Now().After(end) {
				t.Fatalf("signed Coordinator shutdown did not retire actor %d", actor.PID)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	// Source/owner disappearance is not a substituted Wait=0 assertion: their
	// original handles remain owned by the production parent runtime.
	clean = true
}
