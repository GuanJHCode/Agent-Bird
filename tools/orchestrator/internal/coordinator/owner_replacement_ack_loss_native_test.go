package coordinator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type replacementACKWorkerMarker struct {
	PID       int    `json:"pid"`
	ParentPID int    `json:"parent_pid"`
	Birth     string `json:"birth"`
}

// TestMain dispatches the copied test executable before testing parses argv.
// This makes worker-fixture a controlled worker only; it never runs the
// coordinator test suite in the source-host process.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "worker-fixture" {
		os.Exit(replacementACKWorker(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// replacementACKWorker is the only non-production actor in this fixture. The
// public CLI, source host, owner and replacement transport are production
// binaries and protocols. The worker blocks until the test releases it.
func replacementACKWorker(args []string) int {
	if len(args) != 2 && len(args) != 3 {
		return 21
	}
	var term chan os.Signal
	if len(args) == 3 {
		term = make(chan os.Signal, 1)
		signal.Notify(term, syscall.SIGTERM)
		defer signal.Stop(term)
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		return 22
	}
	raw, _ := json.Marshal(replacementACKWorkerMarker{PID: os.Getpid(), ParentPID: os.Getppid(), Birth: birth})
	marker, err := os.OpenFile(args[0], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 23
	}
	_, writeErr := marker.Write(raw)
	closeErr := marker.Close()
	if writeErr != nil || closeErr != nil {
		return 24
	}
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		select {
		case <-term:
			// Receipt is written before continuing to ignore TERM. The test
			// observes survival; only the product's escalation may end us.
			f, err := os.OpenFile(args[2], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err == nil {
				writeErr := json.NewEncoder(f).Encode(time.Now().UnixNano())
				closeErr := f.Close()
				if writeErr != nil || closeErr != nil {
					return 26
				}
			} else if !errors.Is(err, os.ErrExist) {
				return 27
			}
		default:
		}
		if _, err := os.Stat(args[1]); err == nil {
			return 0
		}
	}
	return 25
}

// Regression: HEAD 28c3017 abandoned the original worker after each of
// these committed replies was lost. The same-epoch repair must preserve
// the exact original owner, child, worker, attempt and durable recovery budget.
func TestNativeReplacementSurvivesOneCommittedReplyLoss(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native source-host fixture requires macOS")
	}
	for _, lostKind := range []string{"ticket", "attach", "applied", "activate"} {
		t.Run(lostKind, func(t *testing.T) { nativeReplacementACKLoss(t, lostKind, false) })
	}
}

func TestNativeReplacementReplyLossAcrossCoordinatorRestart(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native source-host fixture requires macOS")
	}
	for _, lostKind := range []string{"ticket", "attach", "applied", "activate"} {
		t.Run(lostKind, func(t *testing.T) { nativeReplacementACKLoss(t, lostKind, true) })
	}
}

func TestNativeReplacementExchangeReplyLoss(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native source-host fixture requires macOS")
	}
	t.Run("same_epoch", func(t *testing.T) { nativeReplacementACKLoss(t, "exchange", false) })
	t.Run("hard_restart", func(t *testing.T) { nativeReplacementACKLoss(t, "exchange", true) })
}

type replacementACKFaultPlan struct {
	losses    int
	exhaust   bool
	stopCause string
}

type replacementACKDroppedReply struct {
	request contract.OwnerRPCRequest
	at      time.Time
}

func TestNativeReplacementSharedFiveOperationBudget(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native source-host fixture requires macOS")
	}
	t.Run("last_operation_succeeds", func(t *testing.T) { nativeReplacementACKLoss(t, "ticket", false, replacementACKFaultPlan{losses: 4}) })
	t.Run("exhaustion_stops_stubborn_worker", func(t *testing.T) {
		nativeReplacementACKLoss(t, "ticket", false, replacementACKFaultPlan{losses: 5, exhaust: true})
	})
}

func TestNativeOwnerStopTriggersCleanUpStubbornWorker(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native source-host fixture requires macOS")
	}
	for _, cause := range []string{"origin_loss", "authenticated_terminal"} {
		t.Run(cause, func(t *testing.T) {
			nativeReplacementACKLoss(t, "ticket", false, replacementACKFaultPlan{losses: 1, stopCause: cause})
		})
	}
}

func nativeReplacementACKLoss(t *testing.T, lostKind string, restartCoordinator bool, options ...replacementACKFaultPlan) {
	t.Helper()
	plan := replacementACKFaultPlan{losses: 1}
	if len(options) == 1 {
		plan = options[0]
	}
	if plan.losses < 1 || plan.losses > 5 || (plan.exhaust && plan.losses != 5) {
		t.Fatal("invalid fault plan")
	}
	if plan.stopCause != "" && (plan.exhaust || restartCoordinator || plan.losses != 1 || (plan.stopCause != "origin_loss" && plan.stopCause != "authenticated_terminal")) {
		t.Fatal("invalid stop trigger")
	}
	root, err := os.MkdirTemp("/private/tmp", "replacement-ack-loss-")
	if err != nil {
		t.Fatal(err)
	}
	clean := false
	t.Cleanup(func() {
		if clean && !t.Failed() {
			_ = os.RemoveAll(root)
		} else {
			t.Logf("retained replacement ACK-loss runtime: %s", root)
		}
	})
	bin, state := filepath.Join(root, "orchestrator"), filepath.Join(root, "state")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", bin, "../../cmd/orchestrator")
	build.Dir = filepath.Join(".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build public CLI: %v %s", err, out)
	}
	if err = os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := NewCLIServer(state, false)
	if err != nil {
		t.Fatal(err)
	}
	var armed atomic.Bool
	var committedReplies atomic.Int32
	lostReply := make(chan replacementACKDroppedReply, plan.losses)
	drop := func(request contract.OwnerRPCRequest) bool {
		if armed.Load() && request.Kind == lostKind && committedReplies.Add(1) <= int32(plan.losses) {
			lostReply <- replacementACKDroppedReply{request: request, at: time.Now()}
			return true
		}
		return false
	}
	s.ownerRPCReplyHook = drop
	s.ownerTransportExchangeReplyHook = func(ticket contract.OwnerTransportTicket) bool {
		return drop(contract.OwnerRPCRequest{Kind: "exchange", ControlRevision: ticket.Pending.ExpectedControlRevision, RequestID: ticket.Pending.RequestID})
	}
	serveCtx, cancelServe := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func(server *Server, ctx context.Context, done chan<- error) { done <- server.Serve(ctx) }(s, serveCtx, serveDone)
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancelServe()
		select {
		case err := <-serveDone:
			if err != nil {
				t.Errorf("Coordinator Serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Coordinator Serve did not finish")
		}
		if err := s.Close(); err != nil {
			t.Errorf("Coordinator Close: %v", err)
		}
	}
	marker, release := filepath.Join(root, "worker.json"), filepath.Join(root, "release")
	var source contract.OriginalOwnerTransport
	var actors []contract.OriginalOwnerTransport
	fixture := filepath.Join(root, "worker-fixture")
	t.Cleanup(func() {
		// Capture this private run even when submit fails before returning a
		// control capability. The pending child may precede HostHello.
		u := url.URL{Scheme: "file", Path: filepath.Join(state, "state.db"), RawQuery: "mode=ro"}
		if db, err := sql.Open("sqlite", u.String()); err == nil {
			readCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			rows, err := db.QueryContext(readCtx, `SELECT h.pid,h.birth FROM runtime_hosts h WHERE EXISTS (SELECT 1 FROM task_runtime tr JOIN tasks t ON t.id=tr.task_id WHERE tr.host_launch_id=h.launch_id AND t.run_id='replacement-ack-loss') UNION SELECT owner_pid,owner_birth FROM execution_owners WHERE run_id='replacement-ack-loss' AND owner_pid IS NOT NULL`)
			if err == nil {
				for rows.Next() {
					var actor contract.OriginalOwnerTransport
					if rows.Scan(&actor.PID, &actor.Birth) == nil && actor.Validate() == nil {
						actors = append(actors, actor)
					}
				}
				_ = rows.Close()
			}
			cancel()
			_ = db.Close()
		}
		ledgers, _ := filepath.Glob(filepath.Join(state, "host-spool", "*", ".coordinator-recovery.json"))
		for _, path := range ledgers {
			var ledger struct {
				Pending struct {
					Host contract.OriginalOwnerTransport `json:"new_host"`
				} `json:"owner_pending"`
			}
			if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &ledger) == nil && ledger.Pending.Host.Validate() == nil {
				actors = append(actors, ledger.Pending.Host)
			}
		}
		if raw, err := os.ReadFile(marker); err == nil {
			var worker replacementACKWorkerMarker
			if json.Unmarshal(raw, &worker) == nil && worker.PID > 1 && worker.Birth != "" {
				actors = append(actors, contract.OriginalOwnerTransport{PID: worker.PID, Birth: worker.Birth})
			}
		}
		_ = os.WriteFile(release, nil, 0600)
		stop()
		owned := func(actor contract.OriginalOwnerTransport) bool {
			return replacementACKOwned(actor, bin) || replacementACKOwned(actor, fixture)
		}
		for _, actor := range actors {
			if owned(actor) {
				_ = syscall.Kill(actor.PID, syscall.SIGTERM)
			}
		}
		end := time.Now().Add(13 * time.Second)
		for _, actor := range actors {
			for owned(actor) {
				if time.Now().After(end) {
					_ = syscall.Kill(actor.PID, syscall.SIGKILL)
					t.Errorf("task actor %d required forced cleanup; not a Wait receipt", actor.PID)
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
	})

	maximum := 45 * time.Second
	if plan.losses > 1 {
		maximum = 75 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), maximum)
	defer cancel()
	image, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(fixture, image, 0700); err != nil {
		t.Fatal(err)
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	workerArgs := []string{fixture, marker, release}
	termReceipt := filepath.Join(root, "worker-term.json")
	if plan.exhaust || plan.stopCause != "" {
		workerArgs = append(workerArgs, termReceipt)
	}
	request := map[string]any{"run_id": "replacement-ack-loss", "controller_thread": "replacement-ack-thread", "plan_revision": 1, "origin_context_id": "replacement-ack-origin", "origin_pid": os.Getpid(), "origin_birth": birth, "host_generation": "replacement-ack-generation", "delivery_mode": "collect", "tasks": []any{map[string]any{"id": "worker", "max_attempts": 1, "max_active_ms": 60000, "completion_policy": "owner_review", "adapter": map[string]any{"kind": "fake", "args": workerArgs, "directory": root}}}}
	var origin *exec.Cmd
	var originIdentity contract.OriginalOwnerTransport
	originWaited := false
	if plan.stopCause == "origin_loss" {
		// A separate real origin lets the test retain its original Wait while
		// leaving Coordinator and all execution actors untouched at trigger.
		origin = exec.Command("/bin/sleep", "120")
		if err = origin.Start(); err != nil {
			t.Fatal("start controlled origin", err)
		}
		t.Cleanup(func() {
			if !originWaited {
				_ = origin.Process.Kill()
				_ = origin.Wait()
			}
		})
		originIdentity.PID = origin.Process.Pid
		originIdentity.Birth, err = process.Birth(originIdentity.PID)
		if err != nil || !replacementACKOwned(originIdentity, "/bin/sleep") {
			t.Fatal("controlled origin identity", err)
		}
		request["origin_pid"], request["origin_birth"] = originIdentity.PID, originIdentity.Birth
	}
	requestPath := filepath.Join(root, "request.json")
	raw, _ := json.Marshal(request)
	if err = os.WriteFile(requestPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		readyCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		ready, err := ipc.Call(readyCtx, filepath.Join(state, "coordinator.sock"), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindReady, RequestID: "ack-loss-ready"})
		cancel()
		if err == nil && ready.Kind == ipc.KindResponse {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Coordinator not ready", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var submitted struct {
		ControlFile string `json:"control_file"`
	}
	if err = json.Unmarshal(replacementACKRun(t, ctx, bin, "submit", "--state-dir", state, "--request", requestPath), &submitted); err != nil {
		t.Fatal(err)
	}
	var worker replacementACKWorkerMarker
	for deadline := time.Now().Add(12 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if raw, err := os.ReadFile(marker); err == nil && json.Unmarshal(raw, &worker) == nil && worker.PID > 1 {
			break
		}
	}
	if worker.PID <= 1 {
		t.Fatal("initial controlled worker did not start")
	}
	originalWorker := worker
	if raw, err := os.ReadFile(marker); err != nil || len(strings.Fields(string(raw))) == 0 {
		t.Fatal("initial worker marker unavailable", err)
	}
	dbURL := "file:" + filepath.Join(state, "state.db") + "?mode=ro"
	db, err := sql.Open("sqlite", dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.QueryRowContext(ctx, `SELECT h.pid,h.birth FROM runtime_hosts h WHERE EXISTS (SELECT 1 FROM task_runtime tr JOIN tasks t ON t.id=tr.task_id WHERE tr.host_launch_id=h.launch_id AND t.run_id='replacement-ack-loss')`).Scan(&source.PID, &source.Birth); err != nil || source.Validate() != nil {
		t.Fatal("source host identity", err, source)
	}
	if !replacementACKOwned(source, bin) {
		t.Fatal("source host did not match exact fixture executable", source)
	}
	before := replacementACKReadStatus(t, ctx, bin, state, submitted.ControlFile)
	var bindingJSON string
	if err = db.QueryRowContext(ctx, `SELECT original_binding_json FROM execution_owners WHERE run_id='replacement-ack-loss' AND is_current=1`).Scan(&bindingJSON); err != nil {
		t.Fatal(err)
	}
	var binding contract.OriginalOwnerBinding
	if json.Unmarshal([]byte(bindingJSON), &binding) != nil || binding.Owner.PID != worker.ParentPID || binding.FirstHost != source {
		t.Fatal("worker not owned by initial bound owner")
	}
	armed.Store(true)
	if err = syscall.Kill(source.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); ; {
		gone, err := process.IdentityExitedContext(ctx, source.PID, source.Birth)
		if err == nil && gone {
			break
		}
		if err != nil || time.Now().After(deadline) {
			t.Fatal("source transport exit proof", gone, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var firstDrop replacementACKDroppedReply
	select {
	case firstDrop = <-lostReply:
		t.Logf("dropped committed replacement %s reply revision=%d request=%s", firstDrop.request.Kind, firstDrop.request.ControlRevision, firstDrop.request.RequestID)
	case <-time.After(15 * time.Second):
		t.Fatalf("replacement did not reach committed %s reply-loss point", lostKind)
	}
	if restartCoordinator {
		oldEpoch := s.epoch
		// Close sockets/storage before cancellation: cancel first would
		// publish a normal signed shutdown fence instead of a hard restart.
		if err = s.Close(); err != nil {
			t.Fatal("hard close Coordinator", err)
		}
		cancelServe()
		stopped = true // old Serve is consumed below; cleanup must not wait twice
		select {
		case err := <-serveDone:
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
				t.Fatal("old Coordinator Serve receipt", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("old Coordinator Serve did not finish")
		}
		restarted, err := NewCLIServer(state, false)
		if err != nil {
			t.Fatal("restart Coordinator", err)
		}
		s = restarted
		stopped = false
		serveCtx, cancelServe = context.WithCancel(context.Background())
		defer cancelServe()
		serveDone = make(chan error, 1)
		go func(server *Server, ctx context.Context, done chan<- error) { done <- server.Serve(ctx) }(s, serveCtx, serveDone)
		for deadline := time.Now().Add(5 * time.Second); ; {
			readyCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
			ready, readyErr := ipc.Call(readyCtx, filepath.Join(state, "coordinator.sock"), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindReady, RequestID: "ack-loss-restarted"})
			cancel()
			if readyErr == nil && ready.Kind == ipc.KindResponse {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("restarted Coordinator not ready", readyErr)
			}
			time.Sleep(20 * time.Millisecond)
		}
		if s.epoch <= oldEpoch {
			t.Fatal("Coordinator epoch did not advance")
		}
		t.Logf("hard-restarted Coordinator after committed %s reply loss: epoch %d -> %d", lostKind, oldEpoch, s.epoch)
	}
	ledgerPath := filepath.Join(state, "host-spool", binding.Scope.LaunchID, ".coordinator-recovery.json")
	readLedger := func() replacementACKLedger {
		t.Helper()
		var ledger replacementACKLedger
		raw, err := os.ReadFile(ledgerPath)
		if err != nil || json.Unmarshal(raw, &ledger) != nil {
			t.Fatal("recovery ledger unavailable", err)
		}
		return ledger
	}
	pending := readLedger().Pending
	if pending.StartSlot != 1 || pending.Host.Validate() != nil {
		t.Fatal("missing original pending child")
	}
	lastDrop := firstDrop
	for n := 1; n < plan.losses; n++ {
		select {
		case next := <-lostReply:
			if next.request.Kind != firstDrop.request.Kind || next.request.ControlRevision != firstDrop.request.ControlRevision || next.request.RequestID != firstDrop.request.RequestID || !bytes.Equal(next.request.Body, firstDrop.request.Body) {
				t.Fatal("retry changed original pending RPC business request")
			}
			lastDrop = next
		case <-ctx.Done():
			t.Fatal("shared budget did not reach required reply loss", n+1, ctx.Err())
		}
		ledger := readLedger()
		if ledger.Pending.StartSlot != pending.StartSlot || ledger.Pending.Host != pending.Host || ledger.Pending.RequestID != pending.RequestID {
			t.Fatal("reply retry started another child or changed pending slot")
		}
	}
	var stopSession *hostSession
	var beforeStopLedger []byte
	assertBoundedStop := func(triggerAt time.Time) {
		t.Helper()
		deadline := triggerAt.Add(13 * time.Second)
		wantAttempts := plan.losses + 1
		if plan.exhaust {
			wantAttempts = 5
		}
		var termAt time.Time
		survivedTERM := false
		for {
			ledger := readLedger()
			if ledger.Attempts != wantAttempts || ledger.Pending.StartSlot != 1 || ledger.Pending.Host != pending.Host || ledger.Pending.RequestID != pending.RequestID || committedReplies.Load() != int32(wantAttempts) {
				t.Fatalf("stop reset/expanded shared budget or replaced child: %+v replies=%d", ledger, committedReplies.Load())
			}
			if raw, err := os.ReadFile(termReceipt); err == nil && termAt.IsZero() {
				var ns int64
				if json.Unmarshal(raw, &ns) == nil && ns > 0 {
					termAt = time.Unix(0, ns)
				}
			}
			if !termAt.IsZero() && time.Since(termAt) >= 8*time.Second && replacementACKOwned(contract.OriginalOwnerTransport{PID: worker.PID, Birth: worker.Birth}, fixture) {
				survivedTERM = true
			}
			allGone := true
			for _, actor := range []contract.OriginalOwnerTransport{pending.Host, {PID: binding.Owner.PID, Birth: binding.Owner.Birth}, {PID: worker.PID, Birth: worker.Birth}} {
				allGone = allGone && errors.Is(syscall.Kill(actor.PID, 0), syscall.ESRCH)
			}
			if time.Now().After(deadline) {
				t.Fatal("product cleanup exceeded 13 seconds; fixture has not signaled execution actors")
			}
			if allGone {
				if ledger.Unavailable != plan.exhaust || len(ledger.Reservation) != 0 || !survivedTERM || termAt.Before(triggerAt) || termAt.After(triggerAt.Add(2*time.Second)) {
					t.Fatal("stop has no product TERM/escalation/budget proof", ledger.Unavailable, survivedTERM, termAt)
				}
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		spool, err := events.Open(filepath.Join(state, "host-spool", binding.Scope.LaunchID, before.AttemptID, before.SegmentID))
		if err != nil {
			t.Fatal(err)
		}
		observed, err := spool.Read()
		if err != nil {
			t.Fatal(err)
		}
		exits := 0
		for _, event := range observed {
			if event.Kind == contract.EventExited {
				exits++
				if event.Process == nil || event.Process.PID != worker.PID || event.Process.Birth != worker.Birth || event.ExitCode == nil || *event.ExitCode == 0 {
					t.Fatal("stop lost original worker nonzero Wait receipt")
				}
			}
		}
		if exits != 1 {
			t.Fatal("stop did not record exactly one original worker exit", exits)
		}
		var afterMarker replacementACKWorkerMarker
		if raw, err := os.ReadFile(marker); err != nil || json.Unmarshal(raw, &afterMarker) != nil || afterMarker != originalWorker {
			t.Fatal("stop replaced worker")
		}
		var attempts, segments int
		if err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM attempts WHERE task_id='worker'),(SELECT COUNT(*) FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id='worker')`).Scan(&attempts, &segments); err != nil || attempts != 1 || segments != 1 {
			t.Fatal("stop redispatched worker", attempts, segments, err)
		}
		if plan.stopCause != "" {
			// Backoff, every pending phase/proof and its original authority
			// must survive terminal cleanup exactly, not only selected fields.
			if after, err := os.ReadFile(ledgerPath); err != nil || !bytes.Equal(after, beforeStopLedger) {
				t.Fatal("stop changed durable recovery authority", err)
			}
			stopSession.mu.Lock()
			terminalSent := stopSession.terminalSent
			stopSession.mu.Unlock()
			if terminalSent != (plan.stopCause == "authenticated_terminal") {
				t.Fatal("origin/terminal trigger evidence changed", terminalSent)
			}
			readyCtx, cancel := context.WithTimeout(ctx, time.Second)
			ready, readyErr := ipc.Call(readyCtx, filepath.Join(state, "coordinator.sock"), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindReady, RequestID: "stop-trigger-coordinator-still-live"})
			cancel()
			if readyErr != nil || ready.Kind != ipc.KindResponse || s.terminating.Load() {
				t.Fatal("stop trigger was confounded by Coordinator shutdown", readyErr)
			}
			if plan.stopCause == "authenticated_terminal" {
				if currentBirth, err := process.Birth(os.Getpid()); err != nil || currentBirth != birth {
					t.Fatal("authenticated terminal lost original live origin", err)
				}
			}
		}
		t.Logf("stop cause=%q exhausted=%v; product TERM ignored for >=8s then original worker exited; owner/child/worker gone within %s without fixture cleanup signals", plan.stopCause, plan.exhaust, time.Since(triggerAt))
		stop()
		clean = true
	}
	if plan.exhaust {
		assertBoundedStop(lastDrop.at)
		return
	}
	// The required green behavior is a live, ready replacement and the same
	// original worker. HEAD returns from the first transient error before this.
	readyWindow := 15 * time.Second
	if plan.losses > 1 {
		// The fifth reservation alone waits 16s before the actual handshake.
		readyWindow = 30 * time.Second
	}
	for deadline := time.Now().Add(readyWindow); ; time.Sleep(25 * time.Millisecond) {
		status := replacementACKReadStatus(t, ctx, bin, state, submitted.ControlFile)
		if status.HostPID != source.PID && status.HostStatus == "ready" && status.Status != "failed" {
			if raw, err := os.ReadFile(marker); err != nil || json.Unmarshal(raw, &worker) != nil || worker.PID <= 1 {
				t.Fatal("replacement started another worker", err)
			}
			if worker != originalWorker {
				t.Fatalf("replacement changed worker identity: before=%+v after=%+v", originalWorker, worker)
			}
			if liveBirth, err := process.Birth(worker.PID); err != nil || liveBirth != worker.Birth {
				t.Fatal("replacement did not retain original live worker", err)
			}
			if status.AttemptID != before.AttemptID || status.SegmentID != before.SegmentID || status.WorkRevision != before.WorkRevision || status.HostPID != pending.Host.PID || status.HostBirth != pending.Host.Birth {
				t.Fatal("ACK repair replaced execution or original pending child")
			}
			for _, actor := range []contract.OriginalOwnerTransport{pending.Host, {PID: binding.Owner.PID, Birth: binding.Owner.Birth}} {
				if !replacementACKOwned(actor, bin) {
					t.Fatal("original owner or child lost", actor)
				}
			}
			parentRaw, err := exec.CommandContext(ctx, "/bin/ps", "-o", "ppid=", "-p", strconv.Itoa(pending.Host.PID)).Output()
			parent, parseErr := strconv.Atoi(strings.TrimSpace(string(parentRaw)))
			if err != nil || parseErr != nil || parent != binding.Owner.PID || !replacementACKOwned(pending.Host, bin) {
				t.Fatal("replacement is not original owner's direct child", err, parseErr)
			}
			var afterBinding string
			var revision int
			wantRevision := 2
			if restartCoordinator && lostKind != "ticket" && lostKind != "exchange" {
				wantRevision = 3
			}
			if err = db.QueryRowContext(ctx, `SELECT original_binding_json,control_revision FROM execution_owners WHERE run_id='replacement-ack-loss' AND is_current=1`).Scan(&afterBinding, &revision); err != nil || afterBinding != bindingJSON || revision != wantRevision {
				t.Fatal("ACK repair changed binding/control revision", revision, err)
			}
			// Activate can already be durable/ready while its lost ACK is still
			// in the owner's required two-second recovery backoff.
			for deadline := time.Now().Add(5 * time.Second); ; {
				ledger := readLedger()
				if ledger.Attempts == plan.losses+1 && !ledger.Unavailable && len(ledger.Reservation) == 0 && ledger.Pending.StartSlot == 1 && ledger.Pending.RequestID == pending.RequestID && ledger.Pending.Host == pending.Host && ledger.Pending.Phase == "activated" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("ACK repair reset/duplicated recovery budget: %+v", ledger)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if plan.stopCause != "" {
				s.mu.Lock()
				stopSession = s.hosts[binding.Scope.ProducerID]
				s.mu.Unlock()
				if stopSession == nil || stopSession.ownerPermit == nil || stopSession.hello.PID != pending.Host.PID || stopSession.hello.Birth != pending.Host.Birth {
					t.Fatal("stop target is not activated replacement")
				}
				beforeStopLedger, err = os.ReadFile(ledgerPath)
				if err != nil || !json.Valid(beforeStopLedger) {
					t.Fatal("snapshot original recovery authority before stop", err)
				}
				triggerAt := time.Now()
				if plan.stopCause == "origin_loss" {
					if !replacementACKOwned(originIdentity, "/bin/sleep") {
						t.Fatal("controlled origin changed before loss trigger")
					}
					if err = origin.Process.Kill(); err != nil {
						t.Fatal("terminate controlled origin", err)
					}
					waitErr := origin.Wait()
					originWaited = true
					var exited *exec.ExitError
					if !errors.As(waitErr, &exited) || !exited.Sys().(syscall.WaitStatus).Signaled() || exited.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL || !errors.Is(syscall.Kill(originIdentity.PID, 0), syscall.ESRCH) {
						t.Fatal("controlled origin lacks original killed Wait/reap", waitErr)
					}
				} else {
					if err := sendHostDisposition(stopSession, s.epoch, "host_retired"); err != nil {
						t.Fatal("write actual signed terminal frame", err)
					}
					t.Logf("signed terminal frame write completed after %s; receipt is proved by subsequent product TERM", time.Since(triggerAt))
				}
				assertBoundedStop(triggerAt)
				return
			}
			if err = os.WriteFile(release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			for complete := time.Now().Add(12 * time.Second); ; time.Sleep(25 * time.Millisecond) {
				terminal := replacementACKReadStatus(t, ctx, bin, state, submitted.ControlFile)
				if terminal.Status == "result_ready" && terminal.SegmentStatus == "exited" {
					break
				}
				if terminal.Status == "failed" || terminal.Status == "unknown" || time.Now().After(complete) {
					t.Fatalf("worker did not complete after replacement %s reply loss: %+v", lostKind, terminal)
				}
			}
			var exits, zeroExits int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*),SUM(CASE WHEN json_extract(body_json,'$.exit_code')=0 AND json_extract(body_json,'$.process.pid')=? AND json_extract(body_json,'$.process.birth')=? THEN 1 ELSE 0 END) FROM runtime_events e JOIN tasks t ON t.id=e.task_id WHERE t.run_id='replacement-ack-loss' AND json_extract(body_json,'$.kind')='exited'`, worker.PID, worker.Birth).Scan(&exits, &zeroExits); err != nil || exits != 1 || zeroExits != 1 || !errors.Is(syscall.Kill(worker.PID, 0), syscall.ESRCH) {
				t.Fatal("worker lacks unique original exit0/reap evidence", exits, zeroExits, err)
			}
			stop()
			for _, actor := range []contract.OriginalOwnerTransport{pending.Host, {PID: binding.Owner.PID, Birth: binding.Owner.Birth}} {
				for deadline := time.Now().Add(13 * time.Second); ; {
					if errors.Is(syscall.Kill(actor.PID, 0), syscall.ESRCH) {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("normal Coordinator shutdown did not retire actor", actor.PID)
					}
					time.Sleep(20 * time.Millisecond)
				}
			}
			clean = true
			return
		}
		if status.Status == "failed" || status.Status == "unknown" || time.Now().After(deadline) {
			t.Fatalf("replacement did not recover after committed %s reply loss: %+v", lostKind, status)
		}
	}
}

type replacementACKLedger struct {
	Attempts    int             `json:"attempts"`
	Unavailable bool            `json:"unavailable"`
	Reservation json.RawMessage `json:"reservation"`
	Pending     struct {
		StartSlot int                             `json:"start_slot"`
		RequestID string                          `json:"request_id"`
		Phase     string                          `json:"phase"`
		Host      contract.OriginalOwnerTransport `json:"new_host"`
	} `json:"owner_pending"`
}

type replacementACKStatus struct {
	HostBirth     string `json:"host_birth"`
	AttemptID     string `json:"attempt_id"`
	SegmentID     string `json:"segment_id"`
	WorkRevision  int    `json:"work_revision"`
	Status        string `json:"status"`
	HostPID       int    `json:"host_pid"`
	HostStatus    string `json:"host_status"`
	SegmentStatus string `json:"segment_status"`
}

func replacementACKReadStatus(t *testing.T, ctx context.Context, bin, state, control string) replacementACKStatus {
	t.Helper()
	var status replacementACKStatus
	if err := json.Unmarshal(replacementACKRun(t, ctx, bin, "status", "--state-dir", state, "--task-id", "worker", "--control-file", control), &status); err != nil {
		t.Fatal(err)
	}
	return status
}

func replacementACKRun(t *testing.T, ctx context.Context, bin string, args ...string) []byte {
	t.Helper()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "ORCHESTRATOR_ENABLE_TEST_FAKE=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", bin, args, err, out)
	}
	return out
}

func replacementACKOwned(identity contract.OriginalOwnerTransport, bin string) bool {
	birth, err := process.Birth(identity.PID)
	if err != nil || birth != identity.Birth {
		return false
	}
	executable, err := process.ExecutablePath(identity.PID)
	return err == nil && executable == bin
}
