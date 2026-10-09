//go:build darwin || linux

package hostbootstrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// Test-only child args select the private per-call checkpoint. No production
// environment variable, global hook or public injection API exists.
func TestPublicationCrashChild(t *testing.T) {
	args := []string{}
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) != 4 {
		t.Skip("owned crash child only")
	}
	state, point := args[0], args[1]
	expected, e := base64.RawStdEncoding.DecodeString(args[2])
	if e != nil {
		t.Fatal(e)
	}
	l, e := Open(context.Background(), state, "launch")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	if _, e = l.Read(); e != nil {
		t.Fatal(e)
	}
	if e = l.PreparePublication(expected, true); e != nil {
		t.Fatal(e)
	}
	e = l.publishPublication(expected, args[3], func(current string) error {
		if current != point {
			return nil
		}
		ready := os.NewFile(3, "ready")
		if _, e := ready.Write([]byte("R")); e != nil {
			return e
		}
		_ = ready.Close()
		for {
			time.Sleep(time.Hour)
		}
	})
	t.Fatalf("checkpoint not reached or publisher returned: %v", e)
}

type publicationNativeCleanup struct {
	root     string
	identity os.FileInfo
	pending  int
}

func (g *publicationNativeCleanup) cleanup(failed bool) (bool, error) {
	if failed || g.pending != 0 {
		return false, nil
	}
	info, e := os.Lstat(g.root)
	if e != nil || !info.IsDir() || !owned(info) || info.Mode().Perm() != 0700 || !os.SameFile(info, g.identity) {
		return false, fmt.Errorf("native cleanup root changed: %v", e)
	}
	e = os.RemoveAll(g.root)
	return e == nil, e
}
func publicationNativeRoot(t *testing.T) (string, *publicationNativeCleanup) {
	t.Helper()
	root, e := os.MkdirTemp("/private/tmp", "agent-bird-publication-stage-")
	if e != nil {
		t.Fatal(e)
	}
	root, e = filepath.EvalSymlinks(root)
	if e != nil {
		t.Fatal(e)
	}
	info, e := os.Lstat(root)
	if e != nil {
		t.Fatal(e)
	}
	g := &publicationNativeCleanup{root: root, identity: info}
	t.Cleanup(func() {
		removed, e := g.cleanup(t.Failed())
		t.Logf("owned_root=%s pending=%d failed=%v removed=%v cleanup_error=%v", root, g.pending, t.Failed(), removed, e)
	})
	return root, g
}
func publicationKillChild(t *testing.T, g *publicationNativeCleanup, point string, expected []byte, priorSHA string) {
	t.Helper()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	readyR, readyW, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer readyR.Close()
	defer readyW.Close()
	cmd := exec.Command(exe, "-test.run=^TestPublicationCrashChild$", "--", g.root, point, base64.RawStdEncoding.EncodeToString(expected), priorSHA)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.ExtraFiles = []*os.File{readyW}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	startErr := cmd.Start()
	if cmd.Process == nil {
		if startErr != nil {
			t.Fatal(startErr)
		}
		t.Fatal("Start returned no original process")
	}
	g.pending++
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Signal(syscall.SIGKILL)
			waitErr := cmd.Wait()
			waited = true
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			treeErr := process.ConfirmGroupExitedContext(ctx, cmd.Process.Pid)
			if cmd.ProcessState != nil && treeErr == nil {
				g.pending--
			}
			t.Logf("cleanup_original_wait=%v tree=%v pid=%d", waitErr, treeErr, cmd.Process.Pid)
		}
	}()
	if startErr != nil {
		t.Fatal("non-nil original process after Start error", startErr)
	}
	_ = readyW.Close()
	if e = readyR.SetReadDeadline(time.Now().Add(10 * time.Second)); e != nil {
		t.Fatal(e)
	}
	var ready [1]byte
	if _, e = io.ReadFull(readyR, ready[:]); e != nil || ready[0] != 'R' {
		t.Fatal("original child checkpoint not ready", point, e)
	}
	pgid, e := syscall.Getpgid(cmd.Process.Pid)
	if e != nil || pgid != cmd.Process.Pid {
		t.Fatal("original child group unknown", pgid, e)
	}
	if e = cmd.Process.Signal(syscall.SIGKILL); e != nil {
		t.Fatal("original process SIGKILL", e)
	}
	e = cmd.Wait()
	waited = true
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	treeErr := process.ConfirmGroupExitedContext(ctx, pgid)
	if cmd.ProcessState != nil && treeErr == nil {
		g.pending--
	}
	var exited *exec.ExitError
	if !errors.As(e, &exited) || !exited.ProcessState.Sys().(syscall.WaitStatus).Signaled() || exited.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL || treeErr != nil {
		t.Fatal("original Wait/tree not proved", e, treeErr, output.String())
	}
	t.Logf("checkpoint=%s original_pid=%d original_wait=SIGKILL tree=exited", point, cmd.Process.Pid)
}
func TestPublicationNativeSIGKILLExactStageReplay(t *testing.T) {
	for _, point := range []string{"created", "partial_write", "file_synced", "pre_rename"} {
		t.Run(point, func(t *testing.T) {
			root, guard := publicationNativeRoot(t)
			l, prior, expected, _, _ := publicationFixtureAt(t, root)
			stageName := l.publicationName(expected)
			l.Close()
			var first os.FileInfo
			for attempt := 0; attempt < 2; attempt++ {
				currentPoint := point
				// After successful O_EXCL was killed once, replay reuses that inode and
				// cannot claim a second successful create. Kill the actual replay read.
				if attempt == 1 && point == "created" {
					currentPoint = "stage_stated"
				}
				publicationKillChild(t, guard, currentPoint, expected, publicationHash(prior))
				names, e := os.ReadDir(filepath.Join(root, "host-bootstrap"))
				if e != nil {
					t.Fatal(e)
				}
				count := 0
				for _, entry := range names {
					if strings.HasPrefix(entry.Name(), ".publication-") {
						count++
						if entry.Name() != stageName {
							t.Fatal("different replay stage", entry.Name())
						}
					}
				}
				if count != 1 {
					t.Fatal("SIGKILL created multiple stages", count)
				}
				path := filepath.Join(root, "host-bootstrap", stageName)
				info, e := os.Lstat(path)
				if e != nil || !launchPrivateFile(info) {
					t.Fatal("stage identity", e)
				}
				if first == nil {
					first = info
				} else if !os.SameFile(first, info) {
					t.Fatal("replay allocated a different inode")
				}
				staged, e := os.ReadFile(path)
				if e != nil || len(staged) > len(expected) {
					t.Fatal("stage grew beyond expected", e)
				}
				if !bytes.Equal(staged, expected[:len(staged)]) {
					t.Fatal("stage is not exact prefix")
				}
				if point == "created" && len(staged) != 0 || point == "partial_write" && (len(staged) == 0 || len(staged) >= len(expected)) || (point == "file_synced" || point == "pre_rename") && !bytes.Equal(staged, expected) {
					t.Fatal("crash did not exercise intended point", point, len(staged), len(expected))
				}
				body, e := os.ReadFile(filepath.Join(root, "host-bootstrap", "launch.json"))
				if e != nil || !bytes.Equal(body, prior) {
					t.Fatal("pre-rename crash changed final", e)
				}
			}
			replay, e := Open(context.Background(), root, "launch")
			if e != nil {
				t.Fatal(e)
			}
			defer replay.Close()
			if _, e = replay.Read(); e != nil {
				t.Fatal(e)
			}
			if e = replay.PreparePublication(expected, true); e != nil {
				t.Fatal(e)
			}
			if e = replay.PublishPublication(expected, publicationHash(prior)); e != nil {
				t.Fatal(e)
			}
			if e = replay.VerifyPublication(expected); e != nil {
				t.Fatal(e)
			}
			final, e := os.Stat(filepath.Join(root, "host-bootstrap", "launch.json"))
			if e != nil || !os.SameFile(first, final) {
				t.Fatal("final did not retain original stage inode", e)
			}
			if _, e = os.Lstat(filepath.Join(root, "host-bootstrap", stageName)); !os.IsNotExist(e) {
				t.Fatal("stage survives verified publication", e)
			}
			body, e := replay.Snapshot()
			if e != nil || !bytes.Equal(body, expected) {
				t.Fatal("replay final bytes", e)
			}
		})
	}
}
func TestPublicationNativeCleanupRetainsUnknown(t *testing.T) {
	root, g := publicationNativeRoot(t)
	g.pending = 1
	if removed, e := g.cleanup(false); e != nil || removed {
		t.Fatal("UNKNOWN cleaned root", e)
	}
	if _, e := os.Lstat(root); e != nil {
		t.Fatal("UNKNOWN root lost", e)
	}
	g.pending = 0
	if removed, e := g.cleanup(true); e != nil || removed {
		t.Fatal("failed oracle cleaned evidence", e)
	}
	// The test has started no process; only this local counter is reset.
}
