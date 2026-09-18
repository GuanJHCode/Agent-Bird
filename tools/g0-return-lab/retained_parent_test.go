package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fillRetainedPipe(t *testing.T, writer *os.File) {
	t.Helper()
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
}

// The shell is a real direct parent and deliberately waits rather than execing
// its child. Only fixed test arguments are passed; production never uses a shell.
func launchRetainedWithParent(t *testing.T, dir, nonce, steps, interval string, output *os.File) (*retainedProcess, int) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	cmd := exec.Command("/bin/sh", "-c", `"$1" retained-start --dir "$2" --nonce "$3" --controller-thread "$4" --steps "$5" --interval "$6" & child=$!; printf '%s\n' "$child" >&3; wait "$child"`, "retained-parent", testBinary, dir, nonce, testController, steps, interval)
	cmd.Env = commandAs(testController).Env
	cmd.ExtraFiles = []*os.File{writer}
	p := &retainedProcess{cmd: cmd, done: make(chan struct{}), output: output}
	// A real file avoids coupling the parent's Wait to the orphan's inherited
	// stderr pipe; the child's survival is asserted separately below.
	errFile, err := os.CreateTemp(t.TempDir(), "parent-stderr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { errFile.Close() })
	cmd.Stdout, cmd.Stderr = output, errFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	pidLine := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(reader).ReadString('\n'); pidLine <- line }()
	worker := 0
	select {
	case line := <-pidLine:
		worker, err = strconv.Atoi(strings.TrimSpace(line))
		if err != nil || worker <= 0 {
			t.Fatalf("invalid child PID: %q", line)
		}
	case <-time.After(3 * time.Second):
		cmd.Process.Kill()
		<-p.done
		t.Fatal("parent did not start child")
	}
	t.Cleanup(func() {
		// These are exact self-created process handles/PIDs; no process discovery.
		syscall.Kill(worker, syscall.SIGKILL)
		select {
		case <-p.done:
		default:
			p.cmd.Process.Kill()
			<-p.done
		}
	})
	return p, worker
}

// Catches relying on normal native cleanup/SIGPIPE: killing a Unix parent does
// not itself kill its child, and the test keeps stdout readers open throughout.
func TestRetainedParentDeathStopsWaitingAndBlockedOutput(t *testing.T) {
	for _, phase := range []string{"initial_output", "between_steps", "last_effect_output"} {
		t.Run(phase, func(t *testing.T) {
			dir := privateDir(t)
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			steps, interval := "3", "1s"
			if phase == "initial_output" {
				fillRetainedPipe(t, writer)
			}
			if phase == "last_effect_output" {
				steps, interval = "1", "300ms"
			}
			p, worker := launchRetainedWithParent(t, dir, phase, steps, interval, writer)
			awaitRetained(t, p, dir, 1, 0)
			if got := readStateFixture(t, dir)["worker_pid"]; got != float64(worker) {
				t.Fatalf("wrong process identity: %v vs %d", got, worker)
			}
			wantSteps := 0
			if phase == "between_steps" {
				awaitRetained(t, p, dir, 1, 1)
				wantSteps = 1
			}
			if phase == "last_effect_output" {
				ready := make(chan string, 1)
				go func() { line, _ := bufio.NewReader(reader).ReadString('\n'); ready <- line }()
				select {
				case line := <-ready:
					if decode(t, line)["completed_steps"] != float64(0) {
						t.Fatal(line)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("missing ready")
				}
				fillRetainedPipe(t, writer)
				awaitRetained(t, p, dir, 1, 1)
				wantSteps = 1
				if got := readStateFixture(t, dir)["completed_steps"]; got != float64(0) {
					t.Fatalf("not in final effect/state gap: %v", got)
				}
			}
			if err := p.cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			p.wait(t)
			deadline := time.Now().Add(1500 * time.Millisecond)
			for {
				if err := syscall.Kill(worker, 0); err == syscall.ESRCH {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("orphaned foreground host survived parent death")
				}
				time.Sleep(10 * time.Millisecond)
			}
			state := retainedInspect(t, dir, phase)
			wantStatus := "interrupted"
			if phase == "last_effect_output" {
				wantStatus = "completed"
			}
			if state["status"] != wantStatus || state["effect_count"] != float64(wantSteps) {
				t.Fatalf("parent death did not preserve the committed prefix: %+v", state)
			}
			for n := 1; n <= wantSteps; n++ {
				if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("step-%03d.json", n))); err != nil {
					t.Fatal(err)
				}
			}
			resume := invokeAs(t, testController, 0, retainedResumeArgs(dir, phase, "1", "1")...)
			if resume.err != "" {
				t.Fatal(resume.err)
			}
			final := retainedInspect(t, dir, phase)
			if final["status"] != "completed" {
				t.Fatalf("parent exit checkpoint could not resume: %+v", final)
			}
			wantSegment := float64(2)
			if phase == "last_effect_output" {
				wantSegment = 1
			}
			if final["segment"] != wantSegment {
				t.Fatalf("parent exit resumed wrong segment: %+v", final)
			}
		})
	}
}
