package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// Catches rejecting a healthy exclusive publication if its fixed stage is
// removed after the reader's Stat. The interleaving uses actual inode snapshots.
func TestRetainedPublishedFileRemainsTrustedAfterStageRemoval(t *testing.T) {
	root, err := os.OpenRoot(privateDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	const name, stage = "step-001.json", ".step-001.json.tmp"
	if err := writeNew(root, stage, struct {
		Version int `json:"version"`
	}{1}); err != nil {
		t.Fatal(err)
	}
	if err := root.Link(stage, name); err != nil {
		t.Fatal(err)
	}
	f, err := root.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || before.Sys().(*syscall.Stat_t).Nlink != 2 {
		t.Fatal("two-link publication barrier not reached")
	}
	if err := root.Remove(stage); err != nil {
		t.Fatal(err)
	}
	after, err := f.Stat()
	if err != nil || after.Sys().(*syscall.Stat_t).Nlink != 1 {
		t.Fatal("publication did not finish")
	}
	if !retainedTrustedFile(root, name, before, false) {
		t.Fatal("reader rejected the same healthy inode after stage cleanup")
	}
}

// Catches bounded stdout cancellation escaping into an unbounded final stderr
// write. Native PTY/exec commonly merges both streams into one full pipe.
func TestRetainedSignalExitIsBoundedWithMergedFullStdio(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			dir := privateDir(t)
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			fillRetainedPipe(t, writer)
			p := &retainedProcess{cmd: commandAs(testController, retainedStartArgs(dir, "merged", "2", "1s")...), done: make(chan struct{})}
			p.cmd.Stdout, p.cmd.Stderr = writer, writer
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
			})
			awaitRetained(t, p, dir, 1, 0)
			if err := p.cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			select {
			case <-p.done:
			case <-time.After(time.Second):
				t.Fatal("retained command remained alive on a blocked final diagnostic")
			}
			if p.err == nil {
				t.Fatal("signal stop reported execution success")
			}
			if state := retainedInspect(t, dir, "merged"); state["status"] != "interrupted" || state["completed_steps"] != float64(0) {
				t.Fatalf("signal exit lost checkpoint: %+v", state)
			}
		})
	}
}
