//go:build !windows

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func leaseFixture(t *testing.T) (string, controlCapability, hostBootstrap) {
	t.Helper()
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	b := hostBootstrap{Version: 2, SocketPath: filepath.Join(state, "coordinator.sock"), SpoolRoot: filepath.Join(state, "host-spool", "launch"), ProducerID: "launch", Hello: contract.HostHello{LaunchID: "launch", LaunchToken: "token", OriginContextID: "origin", OriginPID: 42, OriginBirth: "birth", HostGeneration: "G0"}}
	path, err := writeBootstrap(state, b)
	if err != nil {
		t.Fatal(err)
	}
	return state, controlCapability{Version: 2, RunID: "run", ControllerThread: "thread", ControlToken: "control", BootstrapPath: path, HostLaunchID: "launch", HostLaunchToken: "token"}, b
}

func TestHostLaunchLeasePreservesChangedFiles(t *testing.T) {
	for _, fault := range []string{"root_replaced", "lock_replaced", "bootstrap_edited"} {
		t.Run(fault, func(t *testing.T) {
			state, c, b := leaseFixture(t)
			l, err := openHostLaunchLease(context.Background(), state, c)
			if err != nil {
				t.Fatal(err)
			}
			defer l.close()
			if _, err = l.read(c); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(c.BootstrapPath)
			if err != nil {
				t.Fatal(err)
			}
			want := before
			switch fault {
			case "root_replaced":
				old := state + "-saved"
				if err = os.Rename(state, old); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(old) })
				if err = os.Mkdir(state, 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(filepath.Dir(c.BootstrapPath), 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(c.BootstrapPath, before, 0600); err != nil {
					t.Fatal(err)
				}
			case "lock_replaced":
				digest := sha256.Sum256([]byte(c.HostLaunchID))
				path := filepath.Join(state, "host-recovery", hex.EncodeToString(digest[:])+".lock")
				if err = os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "bootstrap_edited":
				want = bytes.Replace(before, []byte("G0"), []byte("external-change"), 1)
				if err = os.WriteFile(c.BootstrapPath, want, 0600); err != nil {
					t.Fatal(err)
				}
			}
			b.Hello.HostGeneration = "G1"
			if err = l.publish(b); err == nil {
				t.Fatal("published over changed recovery authority")
			}
			after, err := os.ReadFile(c.BootstrapPath)
			if err != nil || !bytes.Equal(after, want) {
				t.Fatal("changed file overwritten", err)
			}
			entries, err := os.ReadDir(filepath.Dir(c.BootstrapPath))
			if err != nil || len(entries) != 1 {
				t.Fatal("unexpected publication side effect", entries, err)
			}
		})
	}
	t.Run("cancelled_acquisition", func(t *testing.T) {
		state, c, _ := leaseFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if l, err := openHostLaunchLease(ctx, state, c); !errors.Is(err, context.Canceled) || l != nil {
			t.Fatal("cancelled lease", l, err)
		}
		if _, err := os.Lstat(filepath.Join(state, "host-recovery")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("cancelled acquisition changed state", err)
		}
	})
}
