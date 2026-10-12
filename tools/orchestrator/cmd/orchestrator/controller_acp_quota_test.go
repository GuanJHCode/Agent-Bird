package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestACPCompletionCapacityPreservesExistingReceipts(t *testing.T) {
	for _, dimension := range []string{"count", "bytes"} {
		t.Run(dimension, func(t *testing.T) {
			state, req := acpCompletionFixture(t)
			original, err := persistACPCompletion(state, req)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(original)
			if err != nil {
				t.Fatal(err)
			}
			count, size := 1023, int64(1)
			if dimension == "bytes" {
				count, size = 256, 64*1024
			}
			for i := 0; i < count; i++ {
				name := fmt.Sprintf("receipt-%x.json", sha256.Sum256([]byte(fmt.Sprintf("fixture-%d", i))))
				f, err := os.OpenFile(filepath.Join(filepath.Dir(original), name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if err = f.Truncate(size); err != nil {
					t.Fatal(err)
				}
				if err = f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			other := req
			other.AttemptID += "-next"
			other.RequestID = contract.ACPReceiveRequestID(other)
			if _, err = persistACPCompletion(state, other); err == nil || err.Error() != "controller_acp_completion_capacity_exhausted" {
				t.Fatalf("new completion admitted at %s capacity: %v", dimension, err)
			}
			if again, err := persistACPCompletion(state, req); err != nil || again != original {
				t.Fatalf("exact replay blocked by quota: %v", err)
			}
			after, err := os.ReadFile(original)
			if err != nil || string(after) != string(before) {
				t.Fatal("old receipt changed", err)
			}
		})
	}
}

func TestACPCompletionQuotaLockRefusesUnsafeOrBusy(t *testing.T) {
	for _, kind := range []string{"symlink", "busy"} {
		t.Run(kind, func(t *testing.T) {
			state, req := acpCompletionFixture(t)
			parent := filepath.Join(state, acpCompletionDirectory)
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(parent, acpCompletionQuotaLock)
			want := "controller_acp_completion_busy"
			if kind == "symlink" {
				outside := filepath.Join(state, "foreign")
				if err := os.WriteFile(outside, []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
				want = "controller_acp_completion_invalid"
			} else {
				f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := persistACPCompletion(state, req); err == nil || err.Error() != want {
				t.Fatalf("unsafe/busy lock admitted: %v", err)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 1 || entries[0].Name() != acpCompletionQuotaLock {
				t.Fatal("refusal created receipt", err)
			}
			if kind == "symlink" {
				body, err := os.ReadFile(filepath.Join(state, "foreign"))
				if err != nil || string(body) != "preserve" {
					t.Fatal("foreign target changed", err)
				}
			}
		})
	}
}
