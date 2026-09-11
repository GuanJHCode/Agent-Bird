package main

import (
	"bufio"
	"bytes"
	"encoding/json"
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

func retainedStartArgs(dir, nonce, steps, interval string) []string {
	return []string{"retained-start", "--dir", dir, "--nonce", nonce, "--controller-thread", testController, "--steps", steps, "--interval", interval}
}

func retainedResumeArgs(dir, nonce, revision, segment string) []string {
	return []string{"retained-resume", "--dir", dir, "--nonce", nonce, "--controller-thread", testController, "--revision", revision, "--segment", segment}
}

func retainedInspect(t *testing.T, dir, nonce string) map[string]any {
	t.Helper()
	got := invokeAs(t, "synthetic_observer", 0, "retained-inspect", "--dir", dir, "--nonce", nonce)
	value := decode(t, got.out)
	if len(value) != 14 || value["liveness_checked"] != false || value["revision"] != float64(1) || value["effect_count"] != value["completed_steps"] {
		t.Fatalf("invalid retained snapshot: %s", got.out)
	}
	return value
}

type retainedProcess struct {
	cmd    *exec.Cmd
	done   chan struct{}
	err    error
	stderr bytes.Buffer
	output *os.File
}

func launchRetained(t *testing.T, args ...string) *retainedProcess {
	t.Helper()
	output, err := os.CreateTemp(t.TempDir(), "stdout-")
	if err != nil {
		t.Fatal(err)
	}
	p := &retainedProcess{cmd: commandAs(testController, args...), done: make(chan struct{}), output: output}
	p.cmd.Stdout, p.cmd.Stderr = output, &p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.err = p.cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			p.cmd.Process.Kill()
			<-p.done
		}
		output.Close()
	})
	return p
}

func (p *retainedProcess) wait(t *testing.T) {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("retained foreground process did not exit")
	}
}

func awaitRetained(t *testing.T, p *retainedProcess, dir string, segment, steps int) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for {
		data, err := os.ReadFile(filepath.Join(dir, "retained-state.json"))
		var state map[string]any
		if err == nil && json.Unmarshal(data, &state) == nil && state["segment"] == float64(segment) {
			if steps == 0 {
				return
			}
			if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("step-%03d.json", steps))); err == nil {
				return
			}
		}
		select {
		case <-p.done:
			t.Fatalf("process exited before checkpoint: %v stderr=%s", p.err, p.stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("checkpoint was not published")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func readStateFixture(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "retained-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return decode(t, string(data))
}

func putStateFixture(t *testing.T, dir string, state map[string]any) {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "retained-state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

// Catches restarting a completed segment or rewriting a completed synthetic effect.
func TestRetainedCompletedResumeDoesNotRepeatEffects(t *testing.T) {
	dir := privateDir(t)
	got := invokeAs(t, testController, 0, retainedStartArgs(dir, "complete", "2", "100ms")...)
	lines := strings.Split(strings.TrimSpace(got.out), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected ready and two durable checkpoints: %q", got.out)
	}
	if decode(t, lines[0])["completed_steps"] != float64(0) || decode(t, lines[2])["status"] != "completed" {
		t.Fatal(got.out)
	}
	before, err := os.Stat(filepath.Join(dir, "step-001.json"))
	if err != nil {
		t.Fatal(err)
	}
	resume := invokeAs(t, testController, 0, retainedResumeArgs(dir, "complete", "1", "1")...)
	if decode(t, resume.out)["segment"] != float64(1) {
		t.Fatal("completed resume created a segment")
	}
	state := retainedInspect(t, dir, "complete")
	if state["status"] != "completed" || state["completed_steps"] != float64(2) || state["worker_pid"] == float64(0) {
		t.Fatalf("incomplete snapshot: %+v", state)
	}
	after, _ := os.Stat(filepath.Join(dir, "step-001.json"))
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("completed effect was rewritten")
	}
}

// Catches a graceful exit deleting records, allowing duplicate work, or detaching.
func TestRetainedSIGTERMPreservesCheckpointsAndContinuesSameTask(t *testing.T) {
	dir := privateDir(t)
	p := launchRetained(t, retainedStartArgs(dir, "term", "4", "200ms")...)
	awaitRetained(t, p, dir, 1, 1)
	if group, err := syscall.Getpgid(p.cmd.Process.Pid); err != nil || group == p.cmd.Process.Pid {
		t.Fatalf("retained host detached: group=%d err=%v", group, err)
	}
	before, _ := os.Stat(filepath.Join(dir, "step-001.json"))
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	p.wait(t)
	state := retainedInspect(t, dir, "term")
	if state["status"] != "interrupted" || state["segment"] != float64(1) || state["completed_steps"].(float64) < 1 || state["completed_steps"].(float64) >= 4 {
		t.Fatalf("stop lost progress: %+v", state)
	}
	time.Sleep(250 * time.Millisecond)
	if next := retainedInspect(t, dir, "term"); next["completed_steps"] != state["completed_steps"] {
		t.Fatal("stopped process continued publishing")
	}
	invokeAs(t, testController, 0, retainedResumeArgs(dir, "term", "1", "1")...)
	last := retainedInspect(t, dir, "term")
	if last["status"] != "completed" || last["segment"] != float64(2) || last["effect_count"] != float64(4) {
		t.Fatalf("resume did not continue same task: %+v", last)
	}
	after, _ := os.Stat(filepath.Join(dir, "step-001.json"))
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("resume replaced a checkpoint")
	}
}

// Catches trusting the stale running flag or PID instead of the OS lock.
func TestRetainedSIGKILLRunningRecordCanResumeOnlyAfterLockRelease(t *testing.T) {
	dir := privateDir(t)
	p := launchRetained(t, retainedStartArgs(dir, "kill", "3", "250ms")...)
	awaitRetained(t, p, dir, 1, 1)
	assertError(t, invokeAs(t, testController, 2, retainedResumeArgs(dir, "kill", "1", "1")...), "busy")
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	p.wait(t)
	if readStateFixture(t, dir)["status"] != "running" {
		t.Fatal("SIGKILL unexpectedly rewrote persisted status")
	}
	state := retainedInspect(t, dir, "kill")
	if state["liveness_checked"] != false {
		t.Fatal("running status was presented as liveness proof")
	}
	invokeAs(t, testController, 0, retainedResumeArgs(dir, "kill", "1", "1")...)
	if final := retainedInspect(t, dir, "kill"); final["effect_count"] != float64(3) || final["segment"] != float64(2) {
		t.Fatalf("bad recovery: %+v", final)
	}
}

// Catches splitting a resume lease by replacing the stable lock or ignoring CAS.
func TestRetainedConcurrentResumeStartsOnlyOneNewSegment(t *testing.T) {
	dir := privateDir(t)
	p := launchRetained(t, retainedStartArgs(dir, "concurrent_resume", "2", "300ms")...)
	awaitRetained(t, p, dir, 1, 0)
	p.cmd.Process.Kill()
	p.wait(t)
	outputs := make([]commandOutput, 8)
	var wg sync.WaitGroup
	for i := range outputs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outputs[i] = runAs(testController, retainedResumeArgs(dir, "concurrent_resume", "1", "1")...)
		}(i)
	}
	wg.Wait()
	success := 0
	for _, out := range outputs {
		if out.code == 0 {
			success++
			continue
		}
		if out.code != 2 {
			t.Fatalf("unexpected process status: %+v", out)
		}
		err := decode(t, out.err)["error"]
		if err != "busy" && err != "stale_segment" {
			t.Fatalf("unexpected contention result: %+v", out)
		}
	}
	if success != 1 {
		t.Fatalf("%d resumes succeeded: %+v", success, outputs)
	}
	// The winning resume has now finished; an old CAS must still fail rather
	// than bypassing the segment check through the completed replay branch.
	assertError(t, invokeAs(t, testController, 2, retainedResumeArgs(dir, "concurrent_resume", "1", "1")...), "stale_segment")
	if final := retainedInspect(t, dir, "concurrent_resume"); final["segment"] != float64(2) || final["effect_count"] != float64(2) {
		t.Fatalf("multiple execution segments: %+v", final)
	}
}

func TestRetainedResumeRejectsWrongOwnerRevisionAndSegment(t *testing.T) {
	dir := privateDir(t)
	invokeAs(t, testController, 0, retainedStartArgs(dir, "guards", "1", "100ms")...)
	for _, tc := range []struct {
		thread string
		args   []string
		want   string
	}{
		{"synthetic_other", retainedResumeArgs(dir, "guards", "1", "1"), "thread_mismatch"},
		{testController, retainedResumeArgs(dir, "guards", "2", "1"), "stale_revision"},
		{testController, retainedResumeArgs(dir, "guards", "1", "2"), "stale_segment"},
		{testController, retainedResumeArgs(dir, "other_nonce", "1", "1"), "nonce_mismatch"},
	} {
		assertError(t, invokeAs(t, tc.thread, 2, tc.args...), tc.want)
	}
	other := retainedResumeArgs(dir, "guards", "1", "1")
	other[6] = "synthetic_other"
	assertError(t, invokeAs(t, "synthetic_other", 2, other...), "owner_mismatch")
}

// A literal crash image models publication winning over the mutable checkpoint.
// Catches trusting cached progress and replaying the already committed last step.
func TestRetainedReconcilesCompletedArtifactWithOlderRunningState(t *testing.T) {
	dir := privateDir(t)
	invokeAs(t, testController, 0, retainedStartArgs(dir, "lag", "2", "100ms")...)
	state := readStateFixture(t, dir)
	state["status"], state["completed_steps"] = "running", float64(1)
	putStateFixture(t, dir, state)
	before, _ := os.Stat(filepath.Join(dir, "step-002.json"))
	if got := retainedInspect(t, dir, "lag"); got["status"] != "completed" || got["completed_steps"] != float64(2) {
		t.Fatalf("last artifact was ignored: %+v", got)
	}
	invokeAs(t, testController, 0, retainedResumeArgs(dir, "lag", "1", "1")...)
	if got := retainedInspect(t, dir, "lag"); got["segment"] != float64(1) {
		t.Fatal("completed publication created a new segment")
	}
	after, _ := os.Stat(filepath.Join(dir, "step-002.json"))
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("last step was replayed")
	}
}

// Catches emitting progress before the effect is durable. Backpressure creates a
// real process-death window between last publication and cached-state commit.
func TestRetainedKilledAfterLastEffectBeforeCheckpointResponseDoesNotReplay(t *testing.T) {
	dir := privateDir(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	cmd := commandAs(testController, retainedStartArgs(dir, "last_gap", "1", "300ms")...)
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
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(reader).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if decode(t, line)["completed_steps"] != float64(0) {
			t.Fatal(line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("missing ready response")
	}
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
			t.Fatalf("fill pipe tail: %v", err)
		}
	}
	if err := syscall.SetNonblock(fd, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "step-001.json")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("effect was not committed before response")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if cmd.Wait() == nil {
		t.Fatal("foreground process did not die")
	}
	state := readStateFixture(t, dir)
	if state["status"] != "running" || state["completed_steps"] != float64(0) {
		t.Fatalf("test did not hit publication/checkpoint gap: %+v", state)
	}
	before, _ := os.Stat(filepath.Join(dir, "step-001.json"))
	invokeAs(t, testController, 0, retainedResumeArgs(dir, "last_gap", "1", "1")...)
	after, _ := os.Stat(filepath.Join(dir, "step-001.json"))
	if !os.SameFile(before, after) {
		t.Fatal("lost response repeated effect")
	}
	if final := retainedInspect(t, dir, "last_gap"); final["segment"] != float64(1) || final["effect_count"] != float64(1) {
		t.Fatalf("lost publication mishandled: %+v", final)
	}
}

func TestRetainedRejectsInvalidBoundsBeforeWriting(t *testing.T) {
	for _, tc := range []struct{ steps, interval string }{{"0", "100ms"}, {"33", "100ms"}, {"2", "61s"}, {"1", "99ms"}, {"1", "100.1ms"}, {"1", "bad"}} {
		dir := privateDir(t)
		assertError(t, invokeAs(t, testController, 2, retainedStartArgs(dir, "bounds", tc.steps, tc.interval)...), "invalid_args")
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatal("invalid arguments wrote files")
		}
	}
	dir := privateDir(t)
	args := retainedStartArgs(dir, "owner", "1", "100ms")
	assertError(t, invokeAs(t, "", 2, args...), "thread_mismatch")
	args[6] = ""
	assertError(t, invokeAs(t, "", 2, args...), "invalid_args")
}

func completedRetainedFixture(t *testing.T, nonce string) string {
	t.Helper()
	dir := privateDir(t)
	invokeAs(t, testController, 0, retainedStartArgs(dir, nonce, "1", "100ms")...)
	return dir
}

// Catches trusting corrupt, cross-bound, non-private, linked, or unexpected data.
func TestRetainedRejectsUnsafeStateAndArtifactsWithoutLeaking(t *testing.T) {
	for _, name := range []string{"retained-task.json", "retained-state.json", "step-001.json", "retained.lock"} {
		for _, mutation := range []string{"unknown", "symlink", "hardlink", "public", "corrupt"} {
			t.Run(name+"/"+mutation, func(t *testing.T) {
				dir := completedRetainedFixture(t, "unsafe")
				path := filepath.Join(dir, name)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				switch mutation {
				case "unknown":
					var v map[string]any
					json.Unmarshal(data, &v)
					v["secret"] = "canary_retained_never_print"
					data, _ = json.Marshal(v)
					err = os.WriteFile(path, data, 0600)
				case "corrupt":
					err = os.WriteFile(path, []byte(`{`), 0600)
				case "public":
					err = os.Chmod(path, 0644)
				case "symlink", "hardlink":
					target := filepath.Join(privateDir(t), "foreign.json")
					err = os.Rename(path, target)
					if err == nil {
						if mutation == "symlink" {
							err = os.Symlink(target, path)
						} else {
							err = os.Link(target, path)
						}
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				out := invokeAs(t, testController, 2, retainedResumeArgs(dir, "unsafe", "1", "1")...)
				if out.out != "" || strings.Contains(out.err, "canary_retained_never_print") {
					t.Fatalf("unsafe artifact leaked: %+v", out)
				}
				invokeAs(t, "observer", 2, "retained-inspect", "--dir", dir, "--nonce", "unsafe")
			})
		}
	}
}

func TestRetainedRejectsMissingStepAndImmutablePlanMismatch(t *testing.T) {
	for _, mutation := range []string{"missing_step", "progress_ahead", "owner", "step_segment", "plan_interval"} {
		t.Run(mutation, func(t *testing.T) {
			dir := completedRetainedFixture(t, "mismatch")
			switch mutation {
			case "missing_step":
				if err := os.Remove(filepath.Join(dir, "step-001.json")); err != nil {
					t.Fatal(err)
				}
			case "progress_ahead", "owner":
				state := readStateFixture(t, dir)
				if mutation == "owner" {
					state["controller_thread"] = "synthetic_other"
				} else {
					state["completed_steps"] = float64(2)
				}
				putStateFixture(t, dir, state)
			case "step_segment", "plan_interval":
				name, field, value := "step-001.json", "segment", float64(2)
				if mutation == "plan_interval" {
					name, field, value = "retained-task.json", "interval_ms", float64(120001)
				}
				path := filepath.Join(dir, name)
				data, _ := os.ReadFile(path)
				v := decode(t, string(data))
				v[field] = value
				data, _ = json.Marshal(v)
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			assertError(t, invokeAs(t, testController, 2, retainedResumeArgs(dir, "mismatch", "1", "1")...), "invalid_artifact")
		})
	}
}

// Catches treating an interrupted staging file as an effect or deleting outputs.
func TestRetainedRecoversOnlyKnownUnpublishedStages(t *testing.T) {
	dir := privateDir(t)
	p := launchRetained(t, retainedStartArgs(dir, "stage", "2", "150ms")...)
	awaitRetained(t, p, dir, 1, 0)
	p.cmd.Process.Kill()
	p.wait(t)
	writeFixtureExclusive(t, filepath.Join(dir, ".step-001.json.tmp"), `{`, 0600)
	writeFixtureExclusive(t, filepath.Join(dir, ".retained-state.json.tmp"), `{`, 0600)
	invokeAs(t, testController, 0, retainedResumeArgs(dir, "stage", "1", "1")...)
	if state := retainedInspect(t, dir, "stage"); state["completed_steps"] != float64(2) {
		t.Fatal(state)
	}
	for _, name := range []string{".step-001.json.tmp", ".retained-state.json.tmp"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatal("unpublished staging file survived")
		}
	}
	writeFixtureExclusive(t, filepath.Join(dir, "foreign.json"), `{}`, 0600)
	assertError(t, invokeAs(t, testController, 2, retainedResumeArgs(dir, "stage", "1", "2")...), "unexpected_artifact")
	if _, err := os.Lstat(filepath.Join(dir, "foreign.json")); err != nil {
		t.Fatal("unknown file was deleted")
	}
}
