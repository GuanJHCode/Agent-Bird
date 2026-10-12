package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

func successorPresence(phase, presence, status, command string) contract.HostPresenceResponse {
	return contract.HostPresenceResponse{Status: status, Presence: presence, ExecutionOwnerMode: "supervised-v1", OwnerPhase: phase, SuccessorCommandID: command}
}

func TestResumeSuccessorPresence(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		states []contract.HostPresenceResponse
		starts int
		code   string
	}{
		{"one_start", []contract.HostPresenceResponse{successorPresence("succession_pending", "old_host_exited", "unready", "command"), successorPresence("succession_pending", "old_host_exited", "unready", "command"), successorPresence("intended", "same_host_alive", "starting", "command"), successorPresence("bound", "same_host_alive", "ready", "command")}, 1, ""},
		{"old_receipt", []contract.HostPresenceResponse{successorPresence("succession_pending", "old_host_exited", "unready", "new-command")}, 0, ""},
		{"old_receipt_current_original", []contract.HostPresenceResponse{successorPresence("bound", "same_host_alive", "ready", "")}, 0, ""},
		{"intended_alive", []contract.HostPresenceResponse{successorPresence("intended", "same_host_alive", "starting", "command"), successorPresence("bound", "same_host_alive", "ready", "command")}, 0, ""},
		{"bound_ready", []contract.HostPresenceResponse{successorPresence("bound", "same_host_alive", "ready", "command")}, 0, ""},
		{"intended_dead", []contract.HostPresenceResponse{successorPresence("intended", "old_host_exited", "starting", "command")}, 0, "owner_resume_pending_blocked"},
		{"bound_dead", []contract.HostPresenceResponse{successorPresence("bound", "old_host_exited", "unready", "command")}, 0, "owner_resume_pending_blocked"},
		{"unknown", []contract.HostPresenceResponse{successorPresence("succession_pending", "unknown", "unready", "command")}, 0, "host_recovery_identity_unknown"},
		{"reused", []contract.HostPresenceResponse{successorPresence("succession_pending", "reused", "unready", "command")}, 0, "host_recovery_identity_unknown"},
		{"unregistered", []contract.HostPresenceResponse{successorPresence("succession_pending", "unregistered", "unready", "command")}, 0, "host_recovery_identity_unknown"},
		{"wrong_phase", []contract.HostPresenceResponse{successorPresence("unknown", "old_host_exited", "unready", "command")}, 0, "host_recovery_identity_unknown"},
		{"pending_alive", []contract.HostPresenceResponse{successorPresence("succession_pending", "same_host_alive", "ready", "command")}, 0, "host_recovery_identity_unknown"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			starts, inspections := 0, 0
			err := ensureSuccessorSourceHostPresence(context.Background(), "command", func(context.Context) (contract.HostPresenceResponse, error) {
				index := inspections
				inspections++
				if index >= len(scenario.states) {
					t.Fatal("unexpected additional inspect")
				}
				return scenario.states[index], nil
			}, func(context.Context) error { starts++; return nil })
			if starts != scenario.starts || (scenario.code == "" && err != nil) || (scenario.code != "" && err != codeError(scenario.code)) {
				t.Fatalf("starts=%d err=%v", starts, err)
			}
		})
	}
	t.Run("original_deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		defer cancel()
		starts := 0
		var deadline time.Time
		err := ensureSuccessorSourceHostPresence(ctx, "command", func(inner context.Context) (contract.HostPresenceResponse, error) {
			d, _ := inner.Deadline()
			if deadline.IsZero() {
				deadline = d
			} else if !deadline.Equal(d) {
				t.Fatal("deadline reset")
			}
			return successorPresence("succession_pending", "old_host_exited", "unready", "command"), nil
		}, func(inner context.Context) error {
			starts++
			d, _ := inner.Deadline()
			if !deadline.Equal(d) {
				t.Fatal("start budget reset")
			}
			return nil
		})
		if !errors.Is(err, context.DeadlineExceeded) || starts != 1 {
			t.Fatalf("err=%v starts=%d", err, starts)
		}
	})
	t.Run("start_failure", func(t *testing.T) {
		failure := errors.New("start failed")
		starts := 0
		err := ensureSuccessorSourceHostPresence(context.Background(), "command", func(context.Context) (contract.HostPresenceResponse, error) {
			return successorPresence("succession_pending", "old_host_exited", "unready", "command"), nil
		}, func(context.Context) error { starts++; return failure })
		if err != failure || starts != 1 {
			t.Fatalf("err=%v starts=%d", err, starts)
		}
	})
	t.Run("canceled_no_inspect", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := ensureSuccessorSourceHostPresence(ctx, "command", func(context.Context) (contract.HostPresenceResponse, error) {
			t.Fatal("canceled inspect")
			return contract.HostPresenceResponse{}, nil
		}, func(context.Context) error { t.Fatal("canceled start"); return nil })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("inspect_consumed_deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		err := ensureSuccessorSourceHostPresence(ctx, "command", func(inner context.Context) (contract.HostPresenceResponse, error) {
			<-inner.Done()
			return successorPresence("succession_pending", "old_host_exited", "unready", "command"), nil
		}, func(context.Context) error { t.Fatal("expired observation started host"); return nil })
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("default_five_second_budget", func(t *testing.T) {
		before := time.Now()
		err := ensureSuccessorSourceHostPresence(context.Background(), "command", func(inner context.Context) (contract.HostPresenceResponse, error) {
			deadline, ok := inner.Deadline()
			budget := deadline.Sub(before)
			if !ok || budget < 4900*time.Millisecond || budget > 5100*time.Millisecond {
				t.Fatalf("budget=%s", budget)
			}
			return successorPresence("bound", "same_host_alive", "ready", "command"), nil
		}, func(context.Context) error { t.Fatal("alive start"); return nil })
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestResumeSuccessorCanceledBootstrap(t *testing.T) {
	state, _, cap := resumeCLIFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := ensureSuccessorSourceHostWith(ctx, state, cap, coordinator.ResumeResponse{}, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled recovery touched missing bootstrap: %v", err)
	}
}

func TestResumeSuccessorPresenceStrict(t *testing.T) {
	state, _, cap := resumeCLIFixture(t)
	cap.Version = 1
	cap.HostLaunchID = ""
	cap.HostLaunchToken = ""
	cap.BootstrapPath = filepath.Join(state, "bootstrap.json")
	b := hostBootstrap{Version: 1, SocketPath: filepath.Join(state, "coordinator.sock"), ProducerID: "launch", Hello: contract.HostHello{LaunchID: "launch", LaunchToken: "launch-token"}}
	body, _ := json.Marshal(b)
	if err := os.WriteFile(cap.BootstrapPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	req := coordinator.ResumeRequest{TaskControlRequest: coordinator.TaskControlRequest{TaskID: "task", ControllerThread: cap.ControllerThread, ControlToken: cap.ControlToken, WorkRevision: 2}, CommandID: "command"}
	r, err := validateResumeReceipt(resumeCLIReceipt(t, req, cap, "successor"), req, cap)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{
		"unknown":                  []byte(`{"status":"ready","presence":"same_host_alive","execution_owner_mode":"supervised-v1","owner_phase":"bound","successor_command_id":"command","extra":true}`),
		"duplicate":                []byte(`{"status":"ready","status":"ready","presence":"same_host_alive","execution_owner_mode":"supervised-v1","owner_phase":"bound","successor_command_id":"command"}`),
		"missing_phase":            []byte(`{"status":"ready","presence":"same_host_alive","execution_owner_mode":"supervised-v1","successor_command_id":"command"}`),
		"null_phase":               []byte(`{"status":"ready","presence":"same_host_alive","execution_owner_mode":"supervised-v1","owner_phase":null,"successor_command_id":"command"}`),
		"null_selector":            []byte(`{"status":"ready","presence":"same_host_alive","execution_owner_mode":"supervised-v1","owner_phase":"bound","successor_command_id":null}`),
		"missing_pending_selector": []byte(`{"status":"offline","presence":"old_host_exited","execution_owner_mode":"supervised-v1","owner_phase":"succession_pending"}`),
		"wrong_owner_mode":         []byte(`{"status":"ready","presence":"same_host_alive","execution_owner_mode":"legacy","owner_phase":"bound","successor_command_id":"command"}`),
	} {
		t.Run(name, func(t *testing.T) {
			err := ensureSuccessorSourceHostWith(context.Background(), state, cap, r, func(context.Context, string, ipc.Kind, any) (ipc.Envelope, error) {
				return ipc.Envelope{Payload: raw}, nil
			}, func(context.Context, string, hostBootstrap) error {
				t.Fatal("invalid presence started host")
				return nil
			})
			if err == nil {
				t.Fatal("invalid presence accepted")
			}
		})
	}
}

func TestResumeSuccessorBootstrapSelector(t *testing.T) {
	state, _, cap := resumeCLIFixture(t)
	cap.Version = 1
	cap.HostLaunchID = ""
	cap.HostLaunchToken = ""
	cap.BootstrapPath = filepath.Join(state, "bootstrap.json")
	b := hostBootstrap{Version: 1, SocketPath: filepath.Join(state, "coordinator.sock"), SpoolRoot: filepath.Join(state, "spool"), ProducerID: "launch", Hello: contract.HostHello{LaunchID: "launch", LaunchToken: "launch-token"}}
	original, _ := json.Marshal(b)
	if err := os.WriteFile(cap.BootstrapPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	request := coordinator.ResumeRequest{TaskControlRequest: coordinator.TaskControlRequest{TaskID: "task", ControllerThread: cap.ControllerThread, ControlToken: cap.ControlToken, WorkRevision: 2}, CommandID: "new-selector"}
	receipt, err := validateResumeReceipt(resumeCLIReceipt(t, request, cap, "successor"), request, cap)
	if err != nil {
		t.Fatal(err)
	}
	inspects, starts := 0, 0
	err = ensureSuccessorSourceHostWith(context.Background(), state, cap, receipt, func(_ context.Context, _ string, kind ipc.Kind, input any) (ipc.Envelope, error) {
		if kind != ipc.KindHostStatus || input.(coordinator.HostStatusRequest).LaunchID != receipt.LaunchID {
			t.Fatal("wrong host request")
		}
		inspects++
		p := successorPresence("succession_pending", "old_host_exited", "unready", request.CommandID)
		if inspects > 1 {
			p = successorPresence("bound", "same_host_alive", "ready", request.CommandID)
		}
		raw, _ := json.Marshal(p)
		return ipc.Envelope{Payload: raw}, nil
	}, func(_ context.Context, _ string, supplied hostBootstrap) error {
		starts++
		if supplied.Hello.ResumeCommandID != request.CommandID || supplied.Hello.LaunchToken != b.Hello.LaunchToken {
			t.Fatalf("selector not passed: %+v", supplied.Hello)
		}
		raw, _ := json.Marshal(supplied)
		if string(raw) == string(original) {
			t.Fatal("memory bootstrap unchanged")
		}
		return nil
	})
	if err != nil || starts != 1 {
		t.Fatalf("err=%v starts=%d", err, starts)
	}
	after, err := os.ReadFile(cap.BootstrapPath)
	if err != nil || string(after) != string(original) {
		t.Fatalf("disk bootstrap changed: %v", err)
	}
	t.Run("launch_mismatch", func(t *testing.T) {
		wrong := receipt
		wrong.LaunchID = "other"
		err := ensureSuccessorSourceHostWith(context.Background(), state, cap, wrong, nil, nil)
		if err == nil {
			t.Fatal("launch mismatch accepted")
		}
	})
}
