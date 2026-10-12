//go:build darwin

package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

const codexFinalCaptureOptIn = "root-reviewed-c06-final-capture-v1"

// Test-only channels, no Handle/state/exit/budget injection. The observation
// never returns before explicit release (or the original parent deadline),
// even when the original work context is deliberately canceled at the hook.
type codexFinalCaptureBarrier struct {
	ready   chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func codexNewFinalCaptureBarrier() *codexFinalCaptureBarrier {
	return &codexFinalCaptureBarrier{ready: make(chan struct{}, 1), release: make(chan struct{})}
}
func (b *codexFinalCaptureBarrier) observe(parent context.Context) {
	b.calls.Add(1)
	select {
	case b.ready <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
	case <-parent.Done():
	}
}
func (b *codexFinalCaptureBarrier) allow() { b.once.Do(func() { close(b.release) }) }

func TestManagedCodexFinalCaptureActor(t *testing.T) {
	if os.Getenv("C06_FINAL_CAPTURE_OPT_IN") != codexFinalCaptureOptIn || os.Getenv("C06_FINAL_CAPTURE_EVIDENCE_DIR") == "" {
		t.Skip("ROOT reviewed double opt-in final capture actor")
	}
	root, err := codexAnchorStateRoot(context.Background(), os.Getenv("C06_FINAL_CAPTURE_EVIDENCE_DIR"))
	if err != nil || len(root.Parents) == 0 || root.Parents[len(root.Parents)-1].Mode != 0700 {
		t.Fatal("fresh canonical owned evidence required")
	}
	for _, name := range []string{"context_cancel", "segment_stop", "passive"} {
		if !t.Run(name, func(t *testing.T) { codexRunFinalCaptureActor(t, root, name) }) {
			t.Fatal("first failed actor/cleanup; subsequent cases not started")
		}
	}
}

// Actual private ExecuteLaunch, existing question helper behind the original
// trusted builder, retained original kernel Handle/Wait/tree and publisher.
// Synthetic provider frames/state do not prove native Codex persistence.
func codexRunFinalCaptureActor(t *testing.T, evidence codexStateRoot, name string) {
	t.Helper()
	if name != "context_cancel" && name != "segment_stop" && name != "passive" {
		t.Fatal("closed case required")
	}
	base := filepath.Join(evidence.Path, name)
	if os.Mkdir(base, 0700) != nil {
		t.Fatal("fresh no-reuse case required")
	}
	parent, parentCancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer parentCancel()
	parentDeadline, _ := parent.Deadline()
	deadline, err := codexActorExecutionDeadline(parentDeadline, time.Now())
	if err != nil {
		t.Fatal("zero Start without original cleanup reserve")
	}
	work, workCancel := context.WithDeadline(parent, deadline)
	barrier := codexNewFinalCaptureBarrier()
	phase := "preparation"
	var h *Host
	var p *process.Handle
	var lease *codexHandleLease
	var originalOp *hostStopOperation
	var originalOpContext context.Context
	var originalOpDeadline time.Time
	var originalOpError error
	var reservationBefore time.Time
	reservationStable := false
	stopCalls := 0
	var before map[string]codexSandboxFingerprint
	var preserved []string
	var records []contract.Event
	var result contract.Result
	var runErr error
	readyWait, readyTree, readyNoSeal, readyNoTerminal, sessionBeforeTurn := false, false, false, false, false
	originalStopMatched := false
	type returned struct {
		result contract.Result
		err    error
	}
	done := make(chan returned, 1)
	started, joined := false, false
	g := contract.LaunchCommand{}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	lookup := func() {
		if h == nil {
			return
		}
		h.stopMu.Lock()
		h.mu.Lock()
		if current := h.codexLeases[g.CommandID]; current != nil {
			lease = current
			if current.handle != nil {
				p = current.handle
			}
		}
		h.mu.Unlock()
		h.stopMu.Unlock()
	}
	fdBefore, fdBeforeErr := os.ReadDir("/dev/fd")
	// Installed before the first execution goroutine/Start. Cleanup releases
	// only our private barrier, consumes the same parent and joins the same
	// original operation. It never reconstructs Handle authority from a PID.
	defer func() {
		barrier.allow()
		workCancel()
		for started && !joined && p == nil && parent.Err() == nil {
			lookup()
			if p != nil {
				break
			}
			select {
			case v := <-done:
				result, runErr, joined = v.result, v.err, true
			case <-parent.Done():
			case <-ticker.C:
			}
		}
		lookup()
		if p != nil && p.ConfirmTreeExited() != nil && h != nil {
			home := filepath.Join(h.spoolRoot, g.AttemptID, "codex-state", "home")
			for _, label := range []string{"initialize-release", "turn-release"} {
				if _, e := os.Lstat(codexActorMarker(home, p.Identity().PID, label)); errors.Is(e, os.ErrNotExist) {
					_ = codexActorWriteMarker(home, p.Identity().PID, label)
				}
			}
			// Existing execution cancellation consumes its first stop window;
			// this public follower can only join it within the original parent.
			stopCtx, stopCancel := context.WithDeadline(parent, parentDeadline.Add(-time.Second))
			stopCalls++
			_ = h.StopSegment(stopCtx, g.SegmentID)
			stopCancel()
		}
		if started && !joined {
			select {
			case v := <-done:
				result, runErr, joined = v.result, v.err, true
			case <-parent.Done():
				t.Error("original ExecuteLaunch remains unjoined")
			}
		}
		waited, absent, pid, pgid, exit := false, false, 0, 0, -1
		if p != nil {
			id := p.Identity()
			pid, pgid = id.PID, id.PGID
			waited = p.Wait(parent) == nil
			absent = p.ConfirmTreeExited() == nil
			exit = p.ExitCode()
		}
		unchanged := false
		var after map[string]codexSandboxFingerprint
		if before != nil {
			var e error
			after, e = codexSandboxSnapshot(preserved)
			unchanged = e == nil && reflect.DeepEqual(before, after)
		}
		sealPresent := false
		if h != nil {
			_, e := os.Lstat(filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, "codex-session-seal.json"))
			sealPresent = e == nil
		}
		fdAfter, fdAfterErr := os.ReadDir("/dev/fd")
		kinds := codexHardlinkEventKinds(records)
		stderrSize, stderrHash := 0, ""
		if p != nil {
			stderr := p.Output()
			stderrSize, stderrHash = len(stderr), codexDigest([]byte(stderr))
		}
		body, _ := json.MarshalIndent(map[string]any{"version": 1, "case": name, "phase": phase, "failed": t.Failed(), "public_entry": false, "synthetic_provider_protocol_and_state": true, "codex_executed": false, "model_turns": 0, "hook_calls": barrier.calls.Load(), "ready_original_wait": readyWait, "ready_tree_absent": readyTree, "ready_no_seal": readyNoSeal, "ready_no_terminal": readyNoTerminal, "session_before_turn": sessionBeforeTurn, "original_handle": p != nil, "pid": pid, "pgid": pgid, "original_wait": waited, "tree_absent": absent, "exit_code": exit, "execute_joined": joined, "status": result.Status, "error_class": codexActorErrorClass(runErr), "event_kinds": kinds, "seal_present": sealPresent, "original_stop_present": originalOp != nil, "original_stop_matched": originalStopMatched, "actual_stop_segment_calls": stopCalls, "reservation_stable": reservationStable, "original_stop_deadline": originalOpDeadline, "original_stop_error_class": codexActorErrorClass(originalOpError), "source_references_unchanged": unchanged, "snapshot_before": before, "snapshot_after": after, "stderr_bytes": stderrSize, "stderr_sha256": stderrHash, "fd_observed": fdBeforeErr == nil && fdAfterErr == nil, "fd_before": len(fdBefore), "fd_after": len(fdAfter), "fd_verdict": "observation_only_unisolated_go_runtime", "original_parent_deadline": parentDeadline, "original_work_deadline": deadline}, "", "  ")
		if codexActorWriteFile(filepath.Join(base, "capture-evidence.json"), append(body, '\n'), os.O_EXCL) != nil {
			t.Error("exclusive evidence failed")
		}
		if started && (!joined || p == nil || !waited || !absent) {
			t.Error("original Handle/Wait/tree/Execute proof missing; halt")
		}
		if before != nil && !unchanged {
			t.Error("original synthetic refs changed")
		}
	}()
	for _, dir := range []string{"source", "workspace", "spool"} {
		if os.Mkdir(filepath.Join(base, dir), 0700) != nil {
			t.Fatal("fresh fixture setup")
		}
	}
	source, cwd, spoolRoot := filepath.Join(base, "source"), filepath.Join(base, "workspace"), filepath.Join(base, "spool")
	if codexActorWriteFile(filepath.Join(source, "config.toml"), []byte("model=\"gpt-fixed\"\nmodel_reasoning_effort=\"source-effort\"\n"), os.O_EXCL) != nil || codexActorWriteFile(filepath.Join(source, "auth.json"), []byte("SYNTHETIC_AUTH_ONLY"), os.O_EXCL) != nil || codexActorWriteFile(filepath.Join(cwd, "input.txt"), []byte("SYNTHETIC_READONLY_INPUT\n"), os.O_EXCL) != nil {
		t.Fatal("synthetic files")
	}
	t.Setenv("HOME", base)
	t.Setenv("CODEX_HOME", source)
	t.Setenv("C06_MANAGED_ACTOR_OPT_IN", codexActorOptIn)
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL"} {
		t.Setenv(key, "")
	}
	helper, err := os.Executable()
	if err == nil {
		helper, err = filepath.EvalSymlinks(helper)
	}
	if err != nil {
		t.Fatal("canonical helper")
	}
	helperSHA, err := process.ExecutableDigest(work, helper)
	if err != nil {
		t.Fatal("actual helper digest")
	}
	providerSHA, err := process.ExecutableDigest(work, codexActorProviderPath)
	if err != nil || providerSHA != adapter.CodexSHA256 {
		t.Fatal("independent provider pin; never execute Codex")
	}
	h, err = NewIPC(spoolRoot, "capture-producer")
	if err != nil || h.BindContinuationOwner("capture-owner") != nil {
		t.Fatal("original Host owner")
	}
	profile := adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, CLIModelDefault: true, TimeoutMS: 60000}
	lock := adapter.ProviderLock{Version: 1, Provider: adapter.ProviderCodex, Protocol: adapter.ProtocolID(adapter.ProviderCodex), Binary: adapter.BinaryPin{Path: codexActorProviderPath, Version: adapter.CodexVersion, SHA256: adapter.CodexSHA256}}
	payload, _ := json.Marshal(adapter.InvocationPayload{Kind: "provider", Provider: string(adapter.ProviderCodex), Directory: cwd, Prompt: "synthetic technical question", Profile: &profile, ProviderLock: &lock})
	g = contract.LaunchCommand{RunID: "capture-run", TaskID: "capture-task", AttemptID: "capture-attempt", SegmentID: "segment-1", WorkRevision: 1, PlanRevision: 1, CommandID: "command-1", HostID: "capture-host", ExecutionEpoch: 1, ReservationID: "reservation-1", GrantedActiveMS: 60000, AdapterPayload: payload}
	inv, err := adapter.BuildManagedCodexInvocation(adapter.Request{Provider: adapter.ProviderCodex, Binary: lock.Binary, Lock: &lock, Profile: &profile, CWD: cwd, Prompt: "synthetic technical question"})
	if err != nil {
		t.Fatal("private invocation")
	}
	spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	if err != nil {
		t.Fatal("original spool")
	}
	home := filepath.Join(h.spoolRoot, g.AttemptID, "codex-state", "home")
	phase = "start"
	started = true
	go func() {
		r, e := h.executeLaunch(work, g, inv, codexExecuteControl{actor: codexActorCommand{Path: helper, SHA256: helperSHA, Scenario: "question"}, beforeSealFinalVerify: func() { barrier.observe(parent) }})
		done <- returned{r, e}
	}()
	for p == nil {
		lookup()
		if p != nil {
			break
		}
		select {
		case v := <-done:
			result, runErr, joined = v.result, v.err, true
			t.Fatal("returned before original Handle")
		case <-work.Done():
			t.Fatal("original Handle acquisition deadline")
		case <-ticker.C:
		}
	}
	id := p.Identity()
	view, viewOK := p.DuplexLaunchState()
	identity, _ := json.Marshal(map[string]any{"pid": id.PID, "pgid": id.PGID, "birth_known": id.BirthKnown, "birth": id.Birth, "actual_executable": id.Executable, "actual_executable_sha256": id.ExecutableSHA256, "duplex_view": viewOK, "spawned_at": view.SpawnedAt, "startup_cleanup_present": view.StartupCleanup != nil, "helper_sha256": helperSHA})
	if codexActorWriteFile(filepath.Join(base, "original-handle.json"), append(identity, '\n'), os.O_EXCL) != nil {
		t.Fatal("original identity")
	}
	phase = "initialize"
	if codexActorAwaitMarker(work, home, id.PID, "initialize-ready") != nil {
		t.Fatal("actual initialize ready")
	}
	preserved = []string{source, cwd, spoolRoot, filepath.Join(source, "config.toml"), filepath.Join(source, "auth.json"), filepath.Join(cwd, "input.txt"), filepath.Join(home, "config.toml"), filepath.Join(home, "auth.json"), filepath.Join(spoolRoot, g.AttemptID, g.SegmentID, "codex-origin.json")}
	before, err = codexSandboxSnapshot(preserved)
	if err != nil {
		t.Fatal("actual immutable reference snapshot")
	}
	if codexActorWriteMarker(home, id.PID, "initialize-release") != nil || codexActorAwaitMarker(work, home, id.PID, "turn-ready") != nil {
		t.Fatal("actual thread/turn barrier")
	}
	records, err = spool.Read()
	sessionBeforeTurn = err == nil && codexActorHasEvent(records, contract.EventSession)
	if !sessionBeforeTurn {
		t.Fatal("turn preceded durable original Session")
	}
	thread := filepath.Join(spoolRoot, g.AttemptID, g.SegmentID, "codex-thread.json")
	threadSnap, err := codexSandboxSnapshot([]string{thread})
	if err != nil {
		t.Fatal("original protected thread")
	}
	preserved = append(preserved, thread)
	before[thread] = threadSnap[thread]
	if codexActorWriteMarker(home, id.PID, "turn-release") != nil {
		t.Fatal("actual turn release")
	}
	phase = "capture_ready"
	select {
	case <-barrier.ready:
	case v := <-done:
		result, runErr, joined = v.result, v.err, true
		t.Fatal("returned before final capture barrier")
	case <-work.Done():
		t.Fatal("final capture readiness deadline")
	}
	readyWait, readyTree = p.Wait(parent) == nil, p.ConfirmTreeExited() == nil
	_, sealErr := os.Lstat(filepath.Join(spoolRoot, g.AttemptID, g.SegmentID, "codex-session-seal.json"))
	readyNoSeal = errors.Is(sealErr, os.ErrNotExist)
	records, err = spool.Read()
	readyNoTerminal = err == nil && !codexActorHasEvent(records, contract.EventResult) && !codexActorHasEvent(records, contract.EventQuestion) && !codexActorHasEvent(records, contract.EventFailed) && !codexActorHasEvent(records, contract.EventStopped) && !codexActorHasEvent(records, contract.EventExited)
	h.stopMu.Lock()
	noPriorStop := lease != nil && lease.handle == p && lease.op == nil && h.stopIntents[g.SegmentID] == nil
	h.stopMu.Unlock()
	if !readyWait || !readyTree || p.ExitCode() != 0 || !readyNoSeal || !readyNoTerminal || !noPriorStop || barrier.calls.Load() != 1 {
		t.Fatal("barrier lacks prior original Wait/tree proof or precedes/follows wrong phase")
	}
	h.grantMu.Lock()
	reservationBefore = h.reservations[g.ReservationID]
	h.grantMu.Unlock()
	if reservationBefore.IsZero() {
		t.Fatal("missing actual reservation at capture barrier")
	}
	phase = "action"
	switch name {
	case "context_cancel":
		workCancel()
	case "segment_stop":
		stopDeadline := time.Now().Add(10 * time.Second)
		if deadline.Before(stopDeadline) {
			stopDeadline = deadline
		}
		stopCtx, stopCancel := context.WithDeadline(parent, stopDeadline)
		stopCalls++
		stopErr := h.StopSegment(stopCtx, g.SegmentID)
		h.stopMu.Lock()
		originalOp = lease.op
		if originalOp != nil {
			originalOpContext, originalOpDeadline, originalOpError = originalOp.ctx, originalOp.deadline, originalOp.err
			originalStopMatched = originalOp.handle == p && originalOp.ctx == stopCtx && originalOp.deadline.Equal(stopDeadline) && h.stopIntents[g.SegmentID] != nil && h.stopIntents[g.SegmentID].op == originalOp
		}
		h.stopMu.Unlock()
		stopCancel()
		if stopErr != nil || !originalStopMatched || originalOpError != nil {
			t.Fatal("actual StopSegment did not latch/complete original bounded operation before release")
		}
	}
	barrier.allow()
	phase = "execute_join"
	select {
	case v := <-done:
		result, runErr, joined = v.result, v.err, true
	case <-parent.Done():
		t.Fatal("original ExecuteLaunch join deadline")
	}
	records, err = spool.Read()
	if err != nil {
		t.Fatal("actual terminal events")
	}
	_, sealErr = os.Lstat(filepath.Join(spoolRoot, g.AttemptID, g.SegmentID, "codex-session-seal.json"))
	sealPresent := sealErr == nil
	h.stopMu.Lock()
	h.mu.Lock()
	if name == "segment_stop" {
		originalStopMatched = originalStopMatched && lease.op == originalOp && originalOp.ctx == originalOpContext && originalOp.deadline.Equal(originalOpDeadline) && originalOp.err == originalOpError
	}
	retired := len(h.codexLeases) == 0 && len(h.active) == 0 && len(h.segmentCancels) == 0
	h.mu.Unlock()
	h.stopMu.Unlock()
	h.grantMu.Lock()
	reservationAfter, reservationRetained := h.reservations[g.ReservationID]
	reservationStable = reservationRetained && reservationAfter.Equal(reservationBefore) && len(h.reservations) == 1
	h.grantMu.Unlock()
	if !retired || !reservationStable || !codexActorHasEvent(records, contract.EventExited) || codexActorHasEvent(records, contract.EventUnknown) || codexActorHasEvent(records, contract.EventResult) || barrier.calls.Load() != 1 {
		t.Fatal("terminal proof/ownership/reservation drift")
	}
	want := contract.EventQuestion
	switch name {
	case "context_cancel":
		want = contract.EventFailed
		if runErr == nil || result.Status != "failed" || sealPresent || lease.op != nil || h.stopWasRequested(g.SegmentID) {
			t.Fatal("work cancellation created Stop/Question/seal or changed classification")
		}
		for _, event := range records {
			if event.Kind == contract.EventFailed {
				if event.Artifact == nil {
					t.Fatal("missing bounded failure artifact")
				}
				f, e := os.Open(event.Artifact.Path)
				if e != nil {
					t.Fatal("original failure artifact")
				}
				body, e := io.ReadAll(io.LimitReader(f, 1025))
				closeErr := f.Close()
				if e != nil || closeErr != nil || len(body) > 1024 || string(body) != `{"status":"failed","reason":"codex_history_seal_unavailable"}` {
					t.Fatal("failure reason contract drift")
				}
			}
		}
	case "segment_stop":
		want = contract.EventStopped
		if result.Status != "interrupted" || sealPresent || !originalStopMatched {
			t.Fatal("stop at capture did not join same op/classify Stopped")
		}
	case "passive":
		if runErr != nil || result.Status != "waiting_user" || !sealPresent || lease.op != nil {
			t.Fatal("passive seam broke real Question publisher")
		}
	}
	if !codexActorHasEvent(records, want) {
		t.Fatal("expected exact terminal missing")
	}
	wantKinds := []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventSession, want, contract.EventExited}
	if !reflect.DeepEqual(codexHardlinkEventKinds(records), wantKinds) || result.EventCount != len(records) {
		t.Fatal("actual event order/count drift")
	}
	for _, event := range records[1:] {
		if event.Process == nil || event.Process.PID != id.PID || event.Process.PGID != id.PGID || event.Process.Birth != id.Birth {
			t.Fatal("terminal/source changed original kernel identity")
		}
	}
	exited := records[len(records)-1]
	if exited.ExitCode == nil || *exited.ExitCode != 0 || exited.ActiveMS < 0 || exited.ActiveMS > g.GrantedActiveMS {
		t.Fatal("original exit/active budget evidence drift")
	}
	wantStops := 0
	if name == "segment_stop" {
		wantStops = 1
	}
	if stopCalls != wantStops {
		t.Fatal("unexpected StopSegment/fresh cleanup calls")
	}
	for _, kind := range []string{contract.EventQuestion, contract.EventStopped, contract.EventFailed} {
		if kind != want && codexActorHasEvent(records, kind) {
			t.Fatal("extra terminal event")
		}
	}
	phase = "completed"
}
