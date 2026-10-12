//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"golang.org/x/sys/unix"
)

func TestDiagnosticExecuteRealENOSPCKeepsOriginalOwnership(t *testing.T) {
	mount := os.Getenv("DIAGNOSTICS_ENOSPC_TMPFS")
	if mount == "" {
		t.Skip("requires a dedicated bounded tmpfs")
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(mount, &fs); err != nil {
		t.Fatal(err)
	}
	if fs.Type != unix.TMPFS_MAGIC || fs.Bsize <= 0 || fs.Blocks == 0 || fs.Blocks > uint64((4<<20)/fs.Bsize) {
		t.Fatal("refuse to fill anything except an explicitly selected tmpfs of at most 4 MiB")
	}
	// Only test coordination lives on the normal temporary filesystem. Host
	// state, launch spool and diagnostics all share the deliberately full mount.
	control, err := os.MkdirTemp("/private/tmp", "host-enospc-control-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(control) })
	t.Setenv("TMPDIR", mount)
	h, g, inv, _, _ := nativeDiagnosticLaunch(t, "failure")
	release, ready := filepath.Join(control, "release"), filepath.Join(control, "ready")
	inv.env["DIAGNOSTIC_EXECUTION_RELEASE"] = release
	inv.env["DIAGNOSTIC_EXECUTION_READY"] = ready
	owned, err := os.MkdirTemp(mount, "host-enospc-filler-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(owned) })
	filler := filepath.Join(owned, "filler")
	a := store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}
	var before []contract.Event
	filled := false
	original := nativeDiagnosticObserver(t, h, g, release, func() {
		before, err = h.Collect(context.Background(), a)
		if err != nil || len(before) == 0 || before[len(before)-1].Kind != contract.EventRunning {
			t.Fatalf("no committed Running before ENOSPC: %v %v", before, err)
		}
		f, err := os.OpenFile(filler, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		block := make([]byte, 4096)
		for total := 0; total <= 4<<20; {
			n, err := f.Write(block)
			total += n
			if errors.Is(err, unix.ENOSPC) {
				filled = true
				t.Logf("Host tmpfs filled with %d bytes after durable Running", total)
				return
			}
			if err != nil || n != len(block) {
				t.Fatalf("unexpected filler write: n=%d err=%v", n, err)
			}
		}
		t.Fatal("bounded tmpfs did not return ENOSPC")
	})
	t.Cleanup(func() {
		if *original != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := (*original).Stop(ctx); err != nil {
				t.Error("original process cleanup:", err)
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := h.ExecuteLaunch(ctx, g, inv)
	if !filled || !errors.Is(err, unix.ENOSPC) || result.Status != "unknown" || result.ExitCode != 0 {
		t.Fatalf("full spool must not claim persisted terminal success: filled=%v result=%+v err=%v", filled, result, err)
	}
	assertDiagnosticOriginalExit(t, original)
	if !strings.Contains((*original).Output(), "termination tail") {
		t.Fatal("ENOSPC prevented original pipe drain")
	}
	after, err := h.Collect(context.Background(), a)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("full spool changed committed history: before=%+v after=%+v err=%v", before, after, err)
	}
	p := readNativeDiagnosticPage(t, h, g)
	if p.Snapshot.Closed || !p.Snapshot.Incomplete {
		t.Fatal("failed diagnostics falsely reported complete")
	}
	if err := os.Remove(filler); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewIPC(h.spoolRoot, h.producerID)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := reopened.Collect(context.Background(), a)
	if err != nil || !reflect.DeepEqual(before, replayed) {
		t.Fatalf("reopen invented terminal history: %+v %v", replayed, err)
	}
	t.Logf("original_wait=%d tree_confirmed=true termination_tail=true result=%s committed_last=%s reopened_history_unchanged=true", (*original).ExitCode(), result.Status, before[len(before)-1].Kind)
}
