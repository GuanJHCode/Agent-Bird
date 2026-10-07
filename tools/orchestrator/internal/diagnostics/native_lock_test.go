//go:build darwin || linux

package diagnostics

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestDiagnosticsLockChild(t *testing.T) {
	mode := os.Getenv("DIAGNOSTICS_LOCK_CHILD")
	if mode == "" {
		return
	}
	hash, _ := storageScope().Hash()
	n, _, err := openNamespace(context.Background(), os.Getenv("DIAGNOSTICS_LOCK_ROOT"), hash, mode == "writer")
	if err != nil {
		t.Fatal(err)
	}
	defer n.close()
	if mode == "update" {
		unlock, err := n.lockUpdate(context.Background(), true)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
	}
	fmt.Println("LOCK_READY")
	var b [1]byte
	if _, err := os.Stdin.Read(b[:]); err != nil {
		t.Fatal(err)
	}
}

func TestStorageNativeWriterAndUpdateLocks(t *testing.T) {
	for _, mode := range []string{"writer", "update"} {
		t.Run(mode, func(t *testing.T) {
			root := storageRoot(t)
			w := storageWriter(t, root)
			if err := w.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			r := storageReader(t, root)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestDiagnosticsLockChild$")
			cmd.Env = append(os.Environ(), "DIAGNOSTICS_LOCK_CHILD="+mode, "DIAGNOSTICS_LOCK_ROOT="+root)
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			in, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					_ = in.Close()
					cancel()
					_ = cmd.Wait()
				}
			}()
			scanner := bufio.NewScanner(out)
			if !scanner.Scan() || scanner.Text() != "LOCK_READY" {
				t.Fatalf("child readiness %q %v", scanner.Text(), scanner.Err())
			}
			if mode == "writer" {
				other, err := OpenWriter(context.Background(), root, storageScope())
				if other != nil {
					_ = other.Close(context.Background())
				}
				if !errors.Is(err, ErrLockBusy) {
					t.Fatalf("native writer exclusion %v", err)
				}
				if _, err := r.Read(context.Background(), PageCursor{}, 1); err != nil {
					t.Fatalf("writer lifetime blocked data read %v", err)
				}
			} else {
				readCtx, readCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
				_, err := r.Read(readCtx, PageCursor{}, 1)
				readCancel()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("reader lock wait not bounded %v", err)
				}
			}
			if _, err := in.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			_ = in.Close()
			// Drain stdout before the original Wait; no replacement process probe.
			for scanner.Scan() {
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			waited = true
			if err != nil {
				t.Fatalf("original child Wait %v", err)
			}
			if _, err := r.Read(context.Background(), PageCursor{}, 1); err != nil {
				t.Fatalf("lock not released %v", err)
			}
		})
	}
}
