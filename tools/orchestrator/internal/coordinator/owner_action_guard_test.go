package coordinator

import (
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Protocol fixture seeds a retired owner's interrupted task via normal store
// APIs, then sends resume from the live unrelated non-worker kernel peer.
func TestProtectedOwnerActionRejectsDeadOwnerWithoutRebind(t *testing.T) {
	s, ctx, _ := projectionServer(t)
	receipt, err := s.db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "old-run", ControllerThread: "old-owner", PlanRevision: 1, OriginContextID: "old-context", OriginPID: 99999999, OriginBirth: "retired-owner", DeliveryMode: "collect"}, Host: store.HostLaunchSpec{OriginContextID: "old-context", HostGeneration: "old-generation", Executable: "/private/bin/orchestrator"}, Tasks: []store.TaskSpec{{ID: "old-task", RunID: "old-run", MaxAttempts: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := s.db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "old-context", OriginPID: 99999999, OriginBirth: "retired-owner", HostGeneration: "old-generation", PID: 99999998, Birth: "retired-host", Executable: "/private/bin/orchestrator"}, s.epoch)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := s.db.ClaimReady(ctx, host, s.epoch, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventStopped, contract.EventExited} {
		_, err = s.db.CommitHostEvent(ctx, contract.Event{Version: 1, ProducerID: host, EventID: fmt.Sprint("event-", i), Sequence: int64(i + 1), RunID: "old-run", TaskID: "old-task", AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, CommandID: grant.CommandID, WorkRevision: 1, ExecutionEpoch: s.epoch, Kind: kind, PayloadHash: fmt.Sprintf("%064x", i+1), ActiveMS: 1})
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(TaskControlRequest{TaskID: "old-task", ControllerThread: "old-owner", ControlToken: receipt.ControlToken, WorkRevision: 1})
	response, err := ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.KindResume, RequestID: "new-peer", Payload: raw})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.db.TaskSnapshot(ctx, "old-task")
	if err != nil {
		t.Fatal(err)
	}
	if response.Kind != ipc.KindError || snapshot.Status != "interrupted" {
		t.Fatalf("dead owner action was not rejected without mutation: kind=%s body=%s durable_status=%s", response.Kind, response.Payload, snapshot.Status)
	}
}

func TestProtectedOwnerActionsAuthenticateBeforeBusinessDecision(t *testing.T) {
	s, ctx, _ := projectionServer(t)
	receipt, err := s.db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "dead-run", ControllerThread: "dead-owner", PlanRevision: 1, OriginContextID: "dead-context", OriginPID: 99999999, OriginBirth: "dead-birth", DeliveryMode: "collect"}, Host: store.HostLaunchSpec{OriginContextID: "dead-context", HostGeneration: "dead-generation", Executable: "/private/bin/orchestrator"}, Tasks: []store.TaskSpec{{ID: "dead-task", RunID: "dead-run", MaxAttempts: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []ipc.Kind{ipc.KindResume, ipc.KindRetry, ipc.KindRework, ipc.KindRequestChanges, ipc.KindAccept, ipc.KindAnswer} {
		for _, validToken := range []bool{false, true} {
			name := fmt.Sprintf("%s/token-%t", kind, validToken)
			t.Run(name, func(t *testing.T) {
				token, want := "wrong-token", "owner_mismatch"
				if validToken {
					token, want = receipt.ControlToken, "origin_owner_lost"
				}
				raw, _ := json.Marshal(map[string]any{"task_id": "dead-task", "controller_thread": "dead-owner", "control_token": token, "work_revision": 1})
				response, err := ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: 1, Kind: kind, RequestID: name, Payload: raw})
				if err != nil || response.Kind != ipc.KindError || !strings.Contains(string(response.Payload), want) {
					t.Fatalf("protected action not authenticated: %s %v", response.Payload, err)
				}
				snapshot, err := s.db.TaskSnapshot(ctx, "dead-task")
				if err != nil || snapshot.Status != "ready" {
					t.Fatalf("rejected action mutated: %+v %v", snapshot, err)
				}
			})
		}
	}
}

type ownerActionFixture struct {
	receipt             store.SubmitReceipt
	context, generation string
}

func interruptedOwnerActionFixture(t *testing.T, s *Server, pid int, birth string, local bool) ownerActionFixture {
	t.Helper()
	ctx := context.Background()
	origin, generation := "native-origin", "native-generation"
	if local {
		owner, err := s.db.CreateOwnerGrant(ctx, "owner", pid, birth)
		if err != nil {
			t.Fatal(err)
		}
		origin, generation = owner.OriginContextID, owner.HostGeneration
	}
	receipt, err := s.db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: origin, OriginPID: pid, OriginBirth: birth, DeliveryMode: "collect"}, Host: store.HostLaunchSpec{OriginContextID: origin, HostGeneration: generation, Executable: "/private/bin/orchestrator"}, Tasks: []store.TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 3, MaxActiveMS: 1000}}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := s.db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: origin, OriginPID: pid, OriginBirth: birth, HostGeneration: generation, PID: 99999998, Birth: "fixture-host", Executable: "/private/bin/orchestrator"}, s.epoch)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := s.db.ClaimReady(ctx, host, s.epoch, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventStopped, contract.EventExited} {
		_, err = s.db.CommitHostEvent(ctx, contract.Event{Version: 1, ProducerID: host, EventID: fmt.Sprint("event-", i), Sequence: int64(i + 1), RunID: "run", TaskID: "task", AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, CommandID: grant.CommandID, WorkRevision: 1, ExecutionEpoch: s.epoch, Kind: kind, PayloadHash: fmt.Sprintf("%064x", i+1), ActiveMS: 1})
		if err != nil {
			t.Fatal(err)
		}
	}
	return ownerActionFixture{receipt, origin, generation}
}

func TestProtectedOwnerActionUsesKernelOwnerAndLocalAncestry(t *testing.T) {
	for _, mode := range []string{"local-owner", "local-sibling", "native-delegate", "wrong-birth"} {
		t.Run(mode, func(t *testing.T) {
			s, ctx, birth := projectionServer(t)
			pid := os.Getpid()
			if mode == "local-sibling" || mode == "native-delegate" {
				sibling := exec.Command("/bin/sleep", "60")
				if err := sibling.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = sibling.Process.Kill(); _ = sibling.Wait() }()
				pid = sibling.Process.Pid
				var err error
				birth, err = process.Birth(pid)
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "wrong-birth" {
				birth = "prior-birth-at-same-pid"
			}
			fixture := interruptedOwnerActionFixture(t, s, pid, birth, mode != "native-delegate")
			raw, _ := json.Marshal(TaskControlRequest{TaskID: "task", ControllerThread: "owner", ControlToken: fixture.receipt.ControlToken, WorkRevision: 1})
			response, err := ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.KindResume, RequestID: mode, Payload: raw})
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			if mode == "local-sibling" {
				want = "owner_peer_mismatch"
			}
			if mode == "wrong-birth" {
				want = "origin_owner_lost"
			}
			snapshot, err := s.db.TaskSnapshot(ctx, "task")
			if err != nil {
				t.Fatal(err)
			}
			if want != "" {
				if response.Kind != ipc.KindError || !strings.Contains(string(response.Payload), want) || snapshot.Status != "interrupted" {
					t.Fatalf("owner guard failed: %s status=%s", response.Payload, snapshot.Status)
				}
			} else if response.Kind != ipc.KindResponse || snapshot.Status != "resume_queued" {
				t.Fatalf("valid owner denied: %s status=%s", response.Payload, snapshot.Status)
			}
		})
	}
}

func TestProtectedOwnerActionAllowsExplicitRebindWithoutBudgetReset(t *testing.T) {
	s, ctx, birth := projectionServer(t)
	oldPID, oldBirth := testRetiredOwner(t)
	fixture := interruptedOwnerActionFixture(t, s, oldPID, oldBirth, true)
	before, err := s.db.SummarizeRun(ctx, "task", "owner", fixture.receipt.ControlToken)
	if err != nil {
		t.Fatal(err)
	}
	var next OwnerBindResponse
	decodePayload(t, callServer(t, s.SocketPath(), ipc.KindOwnerBind, OwnerBindRequest{ControllerThread: "owner", OriginPID: os.Getpid(), OriginBirth: birth}), &next)
	callServer(t, s.SocketPath(), ipc.KindRebindOwner, RebindOwnerRequest{RunID: "run", ControllerThread: "owner", ControlToken: fixture.receipt.ControlToken, OriginContextID: fixture.context, OriginPID: next.OriginPID, OriginBirth: next.OriginBirth, HostGeneration: next.HostGeneration, OwnerMode: "local", OwnerCapability: next.OwnerCapability})
	response := callServer(t, s.SocketPath(), ipc.KindResume, TaskControlRequest{TaskID: "task", ControllerThread: "owner", ControlToken: fixture.receipt.ControlToken, WorkRevision: 1})
	if !strings.Contains(string(response.Payload), "resume_queued") {
		t.Fatalf("explicit rebind could not resume: %s", response.Payload)
	}
	after, err := s.db.SummarizeRun(ctx, "task", "owner", fixture.receipt.ControlToken)
	if err != nil {
		t.Fatal(err)
	}
	if before.Tasks[0].Budget == nil || after.Tasks[0].Budget == nil || *before.Tasks[0].Budget != *after.Tasks[0].Budget {
		t.Fatalf("rebind/resume changed original budget: before=%+v after=%+v", before.Tasks[0].Budget, after.Tasks[0].Budget)
	}
}
