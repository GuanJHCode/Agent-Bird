package main

import (
	"bytes"
	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRejectedSubmitCheckpointsBeforeRecoveryEvidenceCleanup(t *testing.T) {
	for _, phase := range []string{"crash-after-checkpoint", "checkpoint-failure", "success", "legacy"} {
		t.Run(phase, func(t *testing.T) {
			root := privateTaskTemp(t)
			handle := filepath.Join(root, "handle.json")
			control := filepath.Join(root, "control.json")
			if err := os.WriteFile(control, []byte("recovery evidence"), 0600); err != nil {
				t.Fatal(err)
			}
			prior := taskHandle{Version: 1, Status: "submitting", ControlFile: control}
			if err := writeExclusiveJSON(handle, prior); err != nil {
				t.Fatal(err)
			}
			checkpoint := func() error {
				if phase == "checkpoint-failure" {
					return errors.New("disk unavailable")
				}
				next := prior
				next.Status = "rejected"
				next.ControlFile = ""
				return replacePrivateJSON(handle, next)
			}
			if phase == "legacy" {
				checkpoint = nil
			}
			cleaned := false
			invoke := func() {
				err := finishSubmitRejection(checkpoint, func() {
					var saved taskHandle
					if phase != "legacy" {
						if err := readPrivateJSON(handle, &saved); err != nil || saved.Status != "rejected" || saved.ControlFile != "" {
							t.Fatalf("cleanup preceded durable rejection: %+v %v", saved, err)
						}
					}
					if phase == "crash-after-checkpoint" {
						panic("injected crash before cleanup")
					}
					cleaned = true
					_ = os.Remove(control)
				})
				if phase == "checkpoint-failure" && (err == nil || err.Error() != "submit_rejection_uncertain") {
					t.Fatalf("lost uncertainty: %v", err)
				}
				if phase != "checkpoint-failure" && err != nil {
					t.Fatal(err)
				}
			}
			if phase == "crash-after-checkpoint" {
				func() {
					defer func() {
						if recover() == nil {
							t.Fatal("missing injected crash")
						}
					}()
					invoke()
				}()
			} else {
				invoke()
			}
			if phase == "checkpoint-failure" || phase == "crash-after-checkpoint" {
				if cleaned {
					t.Fatal("recovery evidence removed")
				}
				if _, err := os.Stat(control); err != nil {
					t.Fatal(err)
				}
			}
			var saved taskHandle
			if err := readPrivateJSON(handle, &saved); err != nil {
				t.Fatal(err)
			}
			if phase == "checkpoint-failure" && (saved.Status != "submitting" || saved.ControlFile != control) {
				t.Fatalf("failed checkpoint misrepresented rejection: %+v", saved)
			}
		})
	}
}

// Exercise the actual submit RPC and on-disk recovery route, not just callback
// ordering. The coordinator rejects an invalid DAG without launching a Host.
func TestSubmitRemoteRejectionRetainsDurableRecoveryAtCrashBoundaries(t *testing.T) {
	for _, phase := range []string{"prepared-checkpoint-failure", "checkpoint-write-failure", "crash-after-checkpoint", "normal"} {
		t.Run(phase, func(t *testing.T) {
			t.Setenv("ORCHESTRATOR_ENABLE_TEST_FAKE", "1")
			root, err := os.MkdirTemp("/private/tmp", "submit-reject-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			state := filepath.Join(root, "state")
			server, err := coordinator.NewServer(state)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			defer server.Close()
			go func() { _ = server.Serve(ctx) }()
			birth, err := process.Birth(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			req := coordinator.SubmitRequest{RunID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: os.Getpid(), OriginBirth: birth, HostGeneration: "generation", Tasks: []coordinator.TaskRequest{{ID: "run-work", MaxAttempts: 1, Dependencies: []string{"absent"}, AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}}}
			request := filepath.Join(root, "request.json")
			if err = writeExclusiveJSON(request, req); err != nil {
				t.Fatal(err)
			}
			handle := filepath.Join(root, "handle.json")
			h := taskHandle{Version: 1, RunID: "run", StateDir: state, TaskIDs: []string{"run-work"}, Status: "submitting"}
			if err = writeExclusiveJSON(handle, h); err != nil {
				t.Fatal(err)
			}
			plan := filepath.Join(root, "plan.json")
			if err = writeExclusiveJSON(plan, taskPlan{RunID: "run", PlanRevision: 1}); err != nil {
				t.Fatal(err)
			}
			planBytes, _ := os.ReadFile(plan)
			reservation := dispatchReservation{Version: 1, RequestSHA256: "request", ControllerThread: "owner", RunID: "run", Phase: "prepared", PlanSHA256: dispatchHash(string(planBytes))}
			if err = writeExclusiveJSON(filepath.Join(root, "reservation.json"), reservation); err != nil {
				t.Fatal(err)
			}
			control, bootstrap := "", ""
			var submitErr error
			invoke := func() {
				submitErr = submitWithPreparedControl(ctx, []string{"--state-dir", state, "--request", request}, &bytes.Buffer{}, func(path string) error {
					control = path
					cap, err := loadControlCapability(path)
					if err != nil {
						return err
					}
					bootstrap = cap.BootstrapPath
					h.ControlFile = path
					if err := replacePrivateJSON(handle, h); err != nil {
						return err
					}
					if phase == "prepared-checkpoint-failure" {
						return errors.New("injected post-rename sync failure")
					}
					return nil
				}, func() error {
					if phase == "checkpoint-write-failure" {
						if err := os.Chmod(handle, 0400); err != nil {
							return err
						}
					}
					next := h
					next.Status = "rejected"
					next.ControlFile = ""
					if err := replacePrivateJSON(handle, next); err != nil {
						return err
					}
					if phase == "crash-after-checkpoint" {
						panic("injected crash after durable rejection")
					}
					h = next
					return nil
				})
			}
			if phase == "crash-after-checkpoint" {
				func() {
					defer func() {
						if recover() == nil {
							t.Fatal("missing crash")
						}
					}()
					invoke()
				}()
			} else {
				invoke()
			}
			if phase == "prepared-checkpoint-failure" && (submitErr == nil || submitErr.Error() != "submit_preparation_uncertain") {
				t.Fatalf("got %v", submitErr)
			}
			if phase == "checkpoint-write-failure" {
				if submitErr == nil || submitErr.Error() != "submit_rejection_uncertain" {
					t.Fatalf("got %v", submitErr)
				}
				if err = os.Chmod(handle, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if control == "" || bootstrap == "" {
				t.Fatalf("test never reached prepared RPC: %v", submitErr)
			}
			_, saved, err := loadDispatchCheckpoint(root, state, reservation)
			if err != nil {
				t.Fatal(err)
			}
			if phase == "checkpoint-write-failure" || phase == "prepared-checkpoint-failure" {
				if saved.Status != "submitting" || saved.ControlFile != control {
					t.Fatalf("lost uncertain route %+v", saved)
				}
			} else if saved.Status != "rejected" || saved.ControlFile != "" {
				t.Fatalf("replay could resubmit %+v", saved)
			}
			for _, path := range []string{control, bootstrap} {
				_, err := os.Stat(path)
				if phase == "normal" {
					if !os.IsNotExist(err) {
						t.Fatalf("rejected material not cleaned %s %v", path, err)
					}
				} else if err != nil {
					t.Fatalf("recovery evidence lost %s %v", path, err)
				}
			}
			// The durable checkpoint can be replayed repeatedly without any task/Host
			// admission. Remote rejection itself never starts a Source Host.
			for i := 0; i < 2; i++ {
				if _, _, err = loadDispatchCheckpoint(root, state, reservation); err != nil {
					t.Fatal(err)
				}
			}
			db, err := store.Open(filepath.Join(state, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var count int
			if err = db.SQL().QueryRow(`SELECT (SELECT COUNT(*) FROM tasks)+(SELECT COUNT(*) FROM host_launches)+(SELECT COUNT(*) FROM runtime_hosts)`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("replay started/admitted work: %d %v", count, err)
			}
		})
	}
}
