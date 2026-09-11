package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const testController = "synthetic_controller_A"

type commandOutput struct {
	code     int
	out, err string
}

func commandAs(thread string, args ...string) *exec.Cmd {
	cmd := command(args...)
	env := make([]string, 0, len(cmd.Env)+1)
	for _, entry := range cmd.Env {
		if !strings.HasPrefix(entry, "CODEX_THREAD_ID=") {
			env = append(env, entry)
		}
	}
	cmd.Env = append(env, "CODEX_THREAD_ID="+thread)
	return cmd
}

func runAs(thread string, args ...string) commandOutput {
	cmd := commandAs(thread, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		code = -1
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		}
	}
	return commandOutput{code, stdout.String(), stderr.String()}
}

func invokeAs(t *testing.T, thread string, code int, args ...string) commandOutput {
	t.Helper()
	got := runAs(thread, args...)
	if got.code != code {
		t.Fatalf("%v: exit=%d want=%d stdout=%q stderr=%q", args, got.code, code, got.out, got.err)
	}
	return got
}

func assertError(t *testing.T, output commandOutput, want string) {
	t.Helper()
	if output.out != "" || decode(t, output.err)["error"] != want {
		t.Fatalf("expected fixed error %s, got %+v", want, output)
	}
}

func startOwned(t *testing.T, nonce, delay string) string {
	t.Helper()
	dir := privateDir(t)
	output := invokeAs(t, testController, 0, "start", "--dir", dir, "--nonce", nonce, "--delay", delay, "--controller-thread", testController)
	started := decode(t, output.out)
	if started["controller_thread"] != testController || started["task_revision"] != float64(1) {
		t.Fatalf("missing immutable producer binding: %s", output.out)
	}
	return dir
}

func completedOwned(t *testing.T, nonce string) (string, string) {
	t.Helper()
	dir := startOwned(t, nonce, "100ms")
	invoke(t, 0, "wait", "--dir", dir, "--nonce", nonce, "--timeout", "3s")
	output := invokeAs(t, "synthetic_observer", 0, "inspect", "--dir", dir, "--nonce", nonce)
	hash, ok := decode(t, output.out)["event_hash"].(string)
	if !ok || len(hash) != 64 {
		t.Fatalf("missing event hash: %s", output.out)
	}
	return dir, hash
}

func ackArgs(dir, nonce, hash, revision, commandID, decision string) []string {
	return []string{"ack", "--dir", dir, "--nonce", nonce, "--controller-thread", testController, "--revision", revision, "--event-hash", hash, "--command-id", commandID, "--decision", decision}
}

func reviseArgs(dir, nonce, revision string) []string {
	return []string{"revise", "--dir", dir, "--nonce", nonce, "--controller-thread", testController, "--revision", revision}
}

// Catches accepting a declared controller that differs from native process context.
func TestOwnedStartRequiresMatchingInjectedThread(t *testing.T) {
	for _, thread := range []string{"", "synthetic_controller_B"} {
		dir := privateDir(t)
		got := invokeAs(t, thread, 2, "start", "--dir", dir, "--nonce", "owner", "--delay", "100ms", "--controller-thread", testController)
		assertError(t, got, "thread_mismatch")
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatal("rejected owner left execution artifacts")
		}
	}
	for _, owner := range []string{"", "../bad", strings.Repeat("x", 65)} {
		got := invokeAs(t, owner, 2, "start", "--dir", privateDir(t), "--nonce", "owner", "--delay", "100ms", "--controller-thread", owner)
		assertError(t, got, "invalid_args")
	}
}

// Catches hashing mutable controller state instead of the producer's task revision.
func TestInspectBindsActualResultAndProducerRevision(t *testing.T) {
	dir, hash := completedOwned(t, "inspect")
	want := fmt.Sprintf("%x", sha256.Sum256([]byte(`{"version":1,"nonce":"inspect","controller_thread":"synthetic_controller_A","task_revision":1,"result":{"version":1,"status":"completed","nonce":"inspect","count":1}}`)))
	if hash != want {
		t.Fatalf("hash=%s want=%s", hash, want)
	}
	invokeAs(t, testController, 0, reviseArgs(dir, "inspect", "1")...)
	output := invokeAs(t, "synthetic_observer", 0, "inspect", "--dir", dir, "--nonce", "inspect")
	got := decode(t, output.out)
	if got["revision"] != float64(2) || got["task_revision"] != float64(1) || got["event_hash"] != hash {
		t.Fatalf("old result was rebound to the new revision: %s", output.out)
	}
	assertError(t, invokeAs(t, testController, 2, ackArgs(dir, "inspect", hash, "1", "late_1", "handled")...), "stale_revision")
	assertError(t, invokeAs(t, testController, 2, ackArgs(dir, "inspect", hash, "2", "late_2", "handled")...), "event_revision_mismatch")
}

// Catches using command IDs or decisions as separate effect slots.
func TestACKDuplicateCommandsUseOneDecisionAndEffectRecord(t *testing.T) {
	for _, decision := range []string{"handled", "waiting_user", "stale", "rejected"} {
		t.Run(decision, func(t *testing.T) {
			dir, hash := completedOwned(t, decision)
			const firstCommand = "00000000-0000-4000-8000-000000000001"
			args := ackArgs(dir, decision, hash, "1", firstCommand, decision)
			first := invokeAs(t, testController, 0, args...)
			got := decode(t, first.out)
			wantEffect := float64(0)
			if decision == "handled" {
				wantEffect = 1
			}
			if got["effect_count"] != wantEffect || got["decision_count"] != float64(1) || got["command_id"] != firstCommand || got["decision"] != decision {
				t.Fatalf("invalid ACK effect: %s", first.out)
			}
			path := filepath.Join(dir, "ack-r1.json")
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, commandID := range []string{firstCommand, "00000000-0000-4000-8000-000000000002"} {
				next := invokeAs(t, testController, 0, ackArgs(dir, decision, hash, "1", commandID, decision)...)
				if next.out != first.out {
					t.Fatalf("duplicate command did not recover original ACK: %s / %s", first.out, next.out)
				}
			}
			after, _ := os.Stat(path)
			if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
				t.Fatal("duplicate ACK rewrote the effect record")
			}
			conflict := "handled"
			if decision == "handled" {
				conflict = "waiting_user"
			}
			assertError(t, invokeAs(t, testController, 2, ackArgs(dir, decision, hash, "1", "command_third", conflict)...), "decision_conflict")
		})
	}
}

func TestACKRejectsOtherThreadHashAndInvalidArguments(t *testing.T) {
	dir, hash := completedOwned(t, "guards")
	args := ackArgs(dir, "guards", hash, "1", "command", "handled")
	assertError(t, invokeAs(t, "synthetic_controller_B", 2, args...), "thread_mismatch")
	other := append([]string(nil), args...)
	other[6] = "synthetic_controller_B"
	assertError(t, invokeAs(t, "synthetic_controller_B", 2, other...), "owner_mismatch")
	assertError(t, invokeAs(t, testController, 2, ackArgs(dir, "guards", strings.Repeat("0", 64), "1", "command", "handled")...), "event_hash_mismatch")
	for _, mutation := range []struct {
		index int
		value string
	}{
		{8, "0"}, {8, "1000001"}, {10, strings.Repeat("A", 64)}, {12, "../bad"}, {14, "unknown"},
	} {
		invalid := append([]string(nil), args...)
		invalid[mutation.index] = mutation.value
		assertError(t, invokeAs(t, testController, 2, invalid...), "invalid_args")
	}
	assertError(t, invokeAs(t, testController, 2, ackArgs(dir, "different_nonce", hash, "1", "command", "handled")...), "nonce_mismatch")
}

func TestRevisionBeforeCompletionAndCancellationRejectOldWork(t *testing.T) {
	dir := startOwned(t, "revision", "800ms")
	pending := invokeAs(t, "synthetic_observer", 0, "inspect", "--dir", dir, "--nonce", "revision")
	if got := decode(t, pending.out); got["event_hash"] != "" || got["status"] != "pending" {
		t.Fatalf("pending work had a completion hash: %s", pending.out)
	}
	invokeAs(t, testController, 0, reviseArgs(dir, "revision", "1")...)
	invoke(t, 0, "wait", "--dir", dir, "--nonce", "revision", "--timeout", "3s")
	inspected := decode(t, invokeAs(t, "synthetic_observer", 0, "inspect", "--dir", dir, "--nonce", "revision").out)
	if inspected["task_revision"] != float64(1) {
		t.Fatal("late producer used a changed revision")
	}
	hash := inspected["event_hash"].(string)
	assertError(t, invokeAs(t, testController, 2, ackArgs(dir, "revision", hash, "2", "late", "handled")...), "event_revision_mismatch")
	cancel := append(reviseArgs(dir, "revision", "2"), "--cancel")
	got := decode(t, invokeAs(t, testController, 0, cancel...).out)
	if got["revision"] != float64(3) || got["cancelled"] != true {
		t.Fatalf("cancel did not advance revision atomically: %+v", got)
	}
	assertError(t, invokeAs(t, testController, 2, ackArgs(dir, "revision", hash, "3", "cancelled", "handled")...), "cancelled")
}

// Fills stdout before spawning ACK, so publication can complete but its response
// cannot. Killing that real process proves recovery after an actual lost response.
func TestACKRecoversAfterCommittedProcessDiesBeforeResponse(t *testing.T) {
	dir, hash := completedOwned(t, "lost_response")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	fd := int(writer.Fd())
	if err := syscall.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	for total := 0; ; {
		n, err := syscall.Write(fd, bytes.Repeat([]byte("x"), 4096))
		total += n
		if err == syscall.EAGAIN {
			break
		}
		if err != nil || total > 1024*1024 {
			t.Fatalf("fill pipe: %v", err)
		}
	}
	for i := 0; ; i++ {
		_, err := syscall.Write(fd, []byte("x"))
		if err == syscall.EAGAIN {
			break
		}
		if err != nil || i > 4096 {
			t.Fatalf("finish filling pipe: %v", err)
		}
	}
	if err := syscall.SetNonblock(fd, false); err != nil {
		t.Fatal(err)
	}
	cmd := commandAs(testController, ackArgs(dir, "lost_response", hash, "1", "lost_first", "handled")...)
	cmd.Stdout = writer
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "ack-r1.json")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ACK did not persist before response")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("ACK process was not killed")
	}
	output := invokeAs(t, testController, 0, ackArgs(dir, "lost_response", hash, "1", "retry_different_id", "handled")...)
	got := decode(t, output.out)
	if got["command_id"] != "lost_first" || got["effect_count"] != float64(1) || got["decision_count"] != float64(1) {
		t.Fatalf("lost response was not recovered: %s", output.out)
	}
}

func TestConcurrentACKsProduceOneEffect(t *testing.T) {
	dir, hash := completedOwned(t, "concurrent")
	outputs := make([]commandOutput, 20)
	var wg sync.WaitGroup
	for i := range outputs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outputs[i] = runAs(testController, ackArgs(dir, "concurrent", hash, "1", fmt.Sprintf("command_%02d", i), "handled")...)
		}(i)
	}
	wg.Wait()
	for _, output := range outputs {
		if output.code != 0 || output.out != outputs[0].out {
			t.Fatalf("concurrent ACK diverged: %+v", outputs)
		}
	}
	got := decode(t, outputs[0].out)
	if got["effect_count"] != float64(1) || got["decision_count"] != float64(1) {
		t.Fatalf("multiple effects: %+v", got)
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "ack-*.json"))
	if len(paths) != 1 {
		t.Fatalf("more than one action slot: %v", paths)
	}
}

func TestACKAndReviseSerializeAndLateRetryFails(t *testing.T) {
	dir, hash := completedOwned(t, "revise_race")
	var ack, revision commandOutput
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ack = runAs(testController, ackArgs(dir, "revise_race", hash, "1", "racing", "handled")...)
	}()
	go func() { defer wg.Done(); revision = runAs(testController, reviseArgs(dir, "revise_race", "1")...) }()
	wg.Wait()
	if revision.code != 0 {
		t.Fatalf("revision failed: %+v", revision)
	}
	if ack.code != 0 {
		assertError(t, ack, "stale_revision")
	}
	assertError(t, invokeAs(t, testController, 2, ackArgs(dir, "revise_race", hash, "1", "late_retry", "handled")...), "stale_revision")
	got := decode(t, invokeAs(t, "synthetic_observer", 0, "inspect", "--dir", dir, "--nonce", "revise_race").out)
	if got["revision"] != float64(2) {
		t.Fatalf("revision did not commit: %+v", got)
	}
}

func TestACKRecoversUnpublishedStageAndRejectsUnsafeLock(t *testing.T) {
	dir, hash := completedOwned(t, "staged")
	writeFixtureExclusive(t, filepath.Join(dir, ".ack-r1.json.tmp"), `{`, 0600)
	invokeAs(t, testController, 0, ackArgs(dir, "staged", hash, "1", "after_crash", "handled")...)
	if _, err := os.Stat(filepath.Join(dir, ".ack-r1.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("uncommitted stage survived recovery")
	}
	if err := os.Chmod(filepath.Join(dir, "control.lock"), 0644); err != nil {
		t.Fatal(err)
	}
	assertError(t, invokeAs(t, testController, 2, ackArgs(dir, "staged", hash, "1", "unsafe_lock", "handled")...), "untrusted_file")
}

func TestControlCommandsShareBoundedOSLock(t *testing.T) {
	dir, hash := completedOwned(t, "held_lock")
	f, err := os.OpenFile(filepath.Join(dir, "control.lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	ops := [][]string{
		ackArgs(dir, "held_lock", hash, "1", "locked", "handled"),
		reviseArgs(dir, "held_lock", "1"),
		{"inspect", "--dir", dir, "--nonce", "held_lock"},
	}
	outputs := make([]commandOutput, len(ops))
	var wg sync.WaitGroup
	started := time.Now()
	for i := range ops {
		wg.Add(1)
		go func(i int) { defer wg.Done(); outputs[i] = runAs(testController, ops[i]...) }(i)
	}
	wg.Wait()
	if elapsed := time.Since(started); elapsed < 4500*time.Millisecond || elapsed > 8*time.Second {
		t.Fatalf("unbounded lock wait: %s", elapsed)
	}
	for _, output := range outputs {
		if output.code != 2 {
			t.Fatalf("operation bypassed held lock: %+v", output)
		}
		assertError(t, output, "lock_timeout")
	}
	if _, err := os.Stat(filepath.Join(dir, "ack-r1.json")); !os.IsNotExist(err) {
		t.Fatal("ACK committed through a held lock")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	invokeAs(t, testController, 0, ackArgs(dir, "held_lock", hash, "1", "released", "handled")...)
}

func TestReviseRecoversStageWithoutDependingOnResultContents(t *testing.T) {
	dir, _ := completedOwned(t, "cancel_corrupt_result")
	// Deliberately corrupt a finished result. Cancellation only changes control
	// state and must remain possible even when a completion cannot be accepted.
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	writeFixtureExclusive(t, filepath.Join(dir, ".control-revise.tmp"), `{`, 0600)
	output := invokeAs(t, testController, 0, append(reviseArgs(dir, "cancel_corrupt_result", "1"), "--cancel")...)
	got := decode(t, output.out)
	if got["revision"] != float64(2) || got["cancelled"] != true {
		t.Fatalf("cancel failed: %s", output.out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".control-revise.tmp")); !os.IsNotExist(err) {
		t.Fatal("revise left interrupted stage")
	}
}

func TestCancelDoesNotKillPendingSyntheticWorker(t *testing.T) {
	dir := startOwned(t, "cancel_pending", "300ms")
	invokeAs(t, testController, 0, append(reviseArgs(dir, "cancel_pending", "1"), "--cancel")...)
	out, _ := invoke(t, 0, "wait", "--dir", dir, "--nonce", "cancel_pending", "--timeout", "3s")
	assertResult(t, out, "cancel_pending", "completed", 1)
	inspected := decode(t, invokeAs(t, "synthetic_observer", 0, "inspect", "--dir", dir, "--nonce", "cancel_pending").out)
	if inspected["cancelled"] != true || inspected["task_revision"] != float64(1) {
		t.Fatalf("invalid cancelled result: %+v", inspected)
	}
	assertError(t, invokeAs(t, testController, 2, ackArgs(dir, "cancel_pending", inspected["event_hash"].(string), "2", "after_cancel", "handled")...), "cancelled")
}
