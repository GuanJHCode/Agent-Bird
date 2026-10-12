package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

type nativeAdmissionReceipt struct {
	Stage     string          `json:"stage"`
	CommandID string          `json:"command_id,omitempty"`
	Terminal  bool            `json:"terminal,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Deadline  bool            `json:"deadline,omitempty"`
	Event     *contract.Event `json:"event,omitempty"`
}

// Only this test binary recognizes the fixture switch. Product arguments,
// configuration and environment gain no fault-injection capability.
func TestOwnerNativeAdmissionChild(t *testing.T) {
	mode := os.Getenv("ORCHESTRATOR_TEST_BLOCKED_ADMISSION")
	if mode != "1" && mode != "publisher" && mode != "durable" {
		return
	}
	control, err := ipc.TakeOwnerControlFD(4)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	raw, err := ipc.ReadOwnerBootstrapPipe(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := contract.DecodeOwnerRuntimeBootstrap(raw)
	if err != nil {
		t.Fatal(err)
	}
	proof := os.NewFile(5, "admission-proof")
	defer proof.Close()
	encoder := json.NewEncoder(proof)
	err = runOriginalOwner(context.Background(), OriginalOwnerConfig{
		Bootstrap: bootstrap,
		Control:   control,
		InvocationProvider: func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
			if mode != "1" {
				executable, err := os.Executable()
				if err != nil {
					return nil, err
				}
				return launchInvocation{args: []string{executable, "-test.run=^TestOwnerNativeStubbornTreeChild$"}, dir: bootstrap.StateDir, env: map[string]string{"ORCHESTRATOR_TEST_OWNER_TREE": "leader", "ORCHESTRATOR_TEST_OWNER_TREE_DIR": bootstrap.StateDir}}, nil
			}
			_ = os.WriteFile(filepath.Join(bootstrap.StateDir, "unexpected-provider"), []byte("invoked"), 0600)
			return nil, errors.New("unexpected provider invocation")
		},
	}, func(e *ownerExecution) {
		if mode != "1" {
			if mode == "durable" {
				e.run.host.afterSpoolAppend = func(event contract.Event) {
					if event.Kind != contract.EventExited {
						return
					}
					if err := encoder.Encode(nativeAdmissionReceipt{Stage: "durable_exit_blocked", CommandID: event.CommandID, Event: &event}); err != nil {
						panic(err)
					}
					// The actual append (file and directory sync) has returned;
					// the caller cannot finish or notify the publisher. There
					// is no release/context path and no physical fsync claim.
					select {}
				}
			}
			rpc := e.cfg.rpc
			e.cfg.rpc = func(ctx context.Context, revision uint64, kind, id string, body any) (contract.OwnerRPCReply, error) {
				reply, err := rpc(ctx, revision, kind, id, body)
				if event, ok := body.(contract.Event); ok && kind == "event" && event.Kind == contract.EventRunning && err == nil && reply.Error == "" {
					var ack contract.DurableAck
					if contract.DecodeOwnerRuntimeStrict(reply.Body, &ack) != nil || ack.Status != "durable" || ack.ProducerID != event.ProducerID || ack.EventID != event.EventID || ack.PayloadHash != event.PayloadHash || ack.AckedThrough < event.Sequence {
						return reply, errors.New("fixture did not receive matching durable Running ACK")
					}
					stage := "publisher_entered"
					if mode == "durable" {
						stage = "durable_running"
					}
					if err := encoder.Encode(nativeAdmissionReceipt{Stage: stage, CommandID: event.CommandID, Event: &event}); err != nil {
						return reply, err
					}
					// Actual publisher holds its serialization lock. The real
					// signed durable reply has returned; this private wrapper
					// never returns, even on cancellation. Not a network or
					// physical filesystem failure simulation.
					if mode == "publisher" {
						select {}
					}
				}
				return reply, err
			}
			return
		}
		e.acceptGrant = func(grant contract.LaunchCommand) (bool, error) {
			if err := encoder.Encode(nativeAdmissionReceipt{Stage: "admission_entered", CommandID: grant.CommandID}); err != nil {
				return false, err
			}
			// Model an uninterruptible admission operation. There is deliberately
			// no release path, context cancellation, provider or filesystem claim.
			select {}
		}
	})
	var terminal sourceTerminalDisposition
	_ = errors.As(err, &terminal)
	if encodeErr := encoder.Encode(nativeAdmissionReceipt{Stage: "owner_returned", Terminal: ownerCommandDisposition(err) == ownerCommandTerminal, Reason: string(terminal), Deadline: errors.Is(err, context.DeadlineExceeded)}); encodeErr != nil {
		t.Fatal(encodeErr)
	}
	// This expected nonzero process exit must retain the actual runtime error.
	// The parent checks the receipt and original Wait, not the test log's text.
	t.Fatal(err)
}

type nativeOwnerTree struct {
	Leader      int    `json:"leader"`
	Child       int    `json:"child"`
	LeaderBirth string `json:"leader_birth,omitempty"`
	ChildBirth  string `json:"child_birth,omitempty"`
}

func nativeOwnerPoll(t *testing.T, duration time.Duration, predicate func() bool, reason string) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(reason)
}

func nativeOwnerWriteJSON(path string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", body, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// Same-group descendants remain alive until the product escalates TERM to KILL.
// There is no release file, test signal or normal exit inside the 13s window.
func TestOwnerNativeStubbornTreeChild(t *testing.T) {
	role := os.Getenv("ORCHESTRATOR_TEST_OWNER_TREE")
	if role != "leader" && role != "descendant" {
		return
	}
	state := os.Getenv("ORCHESTRATOR_TEST_OWNER_TREE_DIR")
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	defer signal.Stop(signals)
	childDone := make(chan error, 1)
	if role == "leader" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		child := exec.Command(executable, "-test.run=^TestOwnerNativeStubbornTreeChild$")
		child.Env = append(os.Environ(), "ORCHESTRATOR_TEST_OWNER_TREE=descendant")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		go func() { childDone <- child.Wait() }()
		nativeOwnerPoll(t, 5*time.Second, func() bool {
			data, err := os.ReadFile(filepath.Join(state, "descendant-ready.json"))
			var pid int
			return err == nil && json.Unmarshal(data, &pid) == nil && pid == child.Process.Pid
		}, "descendant readiness")
		if err := nativeOwnerWriteJSON(filepath.Join(state, "worker-tree.json"), nativeOwnerTree{Leader: os.Getpid(), Child: child.Process.Pid}); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := nativeOwnerWriteJSON(filepath.Join(state, "descendant-ready.json"), os.Getpid()); err != nil {
			t.Fatal(err)
		}
	}
	for {
		select {
		case <-signals:
			if err := nativeOwnerWriteJSON(filepath.Join(state, role+"-term.json"), time.Now().UnixNano()); err != nil {
				t.Fatal(err)
			}
		case err := <-childDone:
			t.Fatal("descendant exited before product group stop", err)
		case <-time.After(5 * time.Minute):
			t.Fatal("fixture exceeded maximum lifetime")
		}
	}
}

// Test-only bridges keep the external integration parent out of host's
// implementation package, while both native children retain private seams.
type NativeAdmissionReceiptForTest = nativeAdmissionReceipt
type NativeOwnerTreeForTest = nativeOwnerTree

func NativeOwnerPollForTest(t *testing.T, duration time.Duration, predicate func() bool, reason string) {
	t.Helper()
	nativeOwnerPoll(t, duration, predicate, reason)
}
func NativeOwnerWriteJSONForTest(path string, value any) error {
	return nativeOwnerWriteJSON(path, value)
}
