//go:build !windows

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/hostbootstrap"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// This is a real fixed historical source package, never a renamed copy of this
// test executable. It qualifies explicit incompatibility, not release support.
func TestOwnerPublicationHistoricalProtocolRejection(t *testing.T) {
	old := os.Getenv("OWNER_PUBLICATION_HISTORICAL_BINARY")
	if old == "" {
		t.Skip("requires retained source3bd025e package binary")
	}
	const want = "c9a09e7c1bde01c20145294ed2f702a15b91bb10138908fadb6cf951470628ba"
	image, err := os.ReadFile(old)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(image)) != want {
		t.Fatal("historical binary is not fixed source3bd025e")
	}
	for _, oldServer := range []bool{true, false} {
		t.Run(fmt.Sprintf("old_server_%v", oldServer), func(t *testing.T) {
			root, e := os.MkdirTemp("/private/tmp", "publication-compat-")
			if e != nil {
				t.Fatal(e)
			}
			root, e = filepath.EvalSymlinks(root)
			if e != nil {
				t.Fatal(e)
			}
			clean := false
			defer func() {
				if clean && !t.Failed() {
					_ = os.RemoveAll(root)
				} else {
					t.Log("retained compatibility fixture", root)
				}
			}()
			state := filepath.Join(root, "state")
			if e = os.Mkdir(state, 0700); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			var oldHandle *process.Handle
			var server *coordinator.Server
			var served chan error
			if oldServer {
				oldHandle, e = process.Start(ctx, process.Command{Path: old, PinnedPath: old, PinnedSHA256: want, Args: []string{"serve", "--state-dir", state}, ExactEnv: true, Env: []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "TMPDIR=/private/tmp"}})
				if e != nil {
					t.Fatal(e)
				}
			} else {
				server, e = coordinator.NewServer(state)
				if e != nil {
					t.Fatal(e)
				}
				served = make(chan error, 1)
				go func() { served <- server.Serve(ctx) }()
			}
			stopped := false
			stop := func() {
				t.Helper()
				if stopped {
					return
				}
				stopped = true
				if oldHandle != nil {
					c, done := context.WithTimeout(context.Background(), 5*time.Second)
					defer done()
					se := oldHandle.Stop(c)
					we := oldHandle.Wait(c)
					te := oldHandle.ConfirmTreeExited()
					if se != nil || we != nil || te != nil {
						t.Errorf("historical original Stop/Wait/tree %v %v %v", se, we, te)
						return
					}
					t.Logf("historical original PID=%d Wait=0/tree confirmed source=3bd025e sha=%s", oldHandle.Identity().PID, want)
				} else {
					cancel()
					_ = server.Close()
					select {
					case <-served:
					case <-time.After(3 * time.Second):
						t.Error("current Coordinator did not join")
						return
					}
				}
				clean = true
			}
			defer stop()
			socket := filepath.Join(state, "coordinator.sock")
			for {
				c, done := context.WithTimeout(ctx, 200*time.Millisecond)
				r, re := ipc.Call(c, socket, ipc.Envelope{Version: 1, Kind: ipc.KindReady, RequestID: "compat-ready"})
				done()
				if re == nil && r.Kind == ipc.KindResponse {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("historical/current ready", re)
				case <-time.After(10 * time.Millisecond):
				}
			}
			birth, e := process.Birth(os.Getpid())
			if e != nil {
				t.Fatal(e)
			}
			owner := filepath.Join(root, "owner.json")
			control := filepath.Join(root, "control.json")
			request := filepath.Join(root, "rebind.json")
			bootstrap := filepath.Join(state, "host-bootstrap", "fixture-launch.json")
			if e = os.Mkdir(filepath.Dir(bootstrap), 0700); e != nil {
				t.Fatal(e)
			}
			// Local files pass client shape checks only; neither side may reach an owner
			// mutation or create a run under the unsupported publication protocol.
			if e = writeExclusiveJSON(owner, store.OwnerGrant{Version: 1, Kind: "local-owner", ID: "fixture-owner", Token: "fixture-token", ControllerThread: "controller", OriginPID: os.Getpid(), OriginBirth: birth, HostGeneration: "next"}); e != nil {
				t.Fatal(e)
			}
			if e = writeExclusiveJSON(control, controlCapability{Version: 2, RunID: "fixture-run", ControllerThread: "controller", ControlToken: "fixture-control", HostLaunchID: "fixture-launch", HostLaunchToken: "fixture-launch-token", BootstrapPath: bootstrap}); e != nil {
				t.Fatal(e)
			}
			if e = writeExclusiveJSON(bootstrap, hostbootstrap.MetadataV2{Version: 2, SocketPath: socket, SpoolRoot: filepath.Join(state, "host-spool", "fixture-launch"), ProducerID: "fixture-launch", Hello: hostbootstrap.HelloMetadata{LaunchID: "fixture-launch", OriginContextID: "origin", OriginPID: 1, OriginBirth: "fixture-old", HostGeneration: "old", Executable: old}}); e != nil {
				t.Fatal(e)
			}
			if e = writeExclusiveJSON(request, map[string]any{"version": 1, "owner_mode": "local", "owner_capability": owner, "control_file": control}); e != nil {
				t.Fatal(e)
			}
			prior, e := os.ReadFile(bootstrap)
			if e != nil {
				t.Fatal(e)
			}
			var child *exec.Cmd
			wantError := "owner_rebind_publication_protocol_required"
			args := []string{"rebind-owner", "--state-dir", state, "--request", request}
			if oldServer {
				exe, e := os.Executable()
				if e != nil {
					t.Fatal(e)
				}
				child = exec.CommandContext(ctx, exe, append([]string{"-test.run=^TestDiagnosticsCLIHelperProcess$", "--"}, args...)...)
				child.Env = append(os.Environ(), "DIAGNOSTICS_CLI_HELPER=cli")
				wantError = "coordinator_upgrade_required"
			} else {
				child = exec.CommandContext(ctx, old, args...)
			}
			output, ce := child.CombinedOutput()
			if ce == nil || child.ProcessState == nil || child.ProcessState.ExitCode() != 2 || !strings.Contains(string(output), wantError) {
				t.Fatal("wrong compatibility result", ce, string(output))
			}
			t.Logf("CLI original Wait=2 protocol=%s", wantError)
			got, e := os.ReadFile(bootstrap)
			if e != nil || !bytes.Equal(got, prior) {
				t.Fatal("unsupported protocol changed bootstrap", e)
			}
			db, e := sql.Open("sqlite", "file:"+filepath.Join(state, "state.db")+"?mode=ro")
			if e != nil {
				t.Fatal(e)
			}
			var runs, rebinds, version int
			e = db.QueryRow(`SELECT (SELECT count(*) FROM runs),(SELECT count(*) FROM owner_rebinds),(SELECT version FROM schema_meta)`).Scan(&runs, &rebinds, &version)
			_ = db.Close()
			expectedVersion := 35
			if oldServer {
				expectedVersion = 34
			}
			if e != nil || runs != 0 || rebinds != 0 || version != expectedVersion {
				t.Fatal("unsupported protocol mutated Store", runs, rebinds, version, e)
			}
			stop()
		})
	}
}
