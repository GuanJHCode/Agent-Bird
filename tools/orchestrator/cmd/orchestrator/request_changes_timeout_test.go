package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
)

type ownerMutationTransport struct {
	state     string
	release   chan struct{}
	mu        sync.Mutex
	requests  []store.OwnerReworkSpec
	effects   map[string]store.OwnerReworkSpec
	source    store.OwnerReworkSpec
	summaries int
}

func newOwnerMutationTransport(t *testing.T, mode string) *ownerMutationTransport {
	t.Helper()
	root, err := os.MkdirTemp("/private/tmp", "owner-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err = os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(state, "coordinator.sock"))
	if err != nil {
		t.Fatal(err)
	}
	s := &ownerMutationTransport{state: state, release: make(chan struct{}), effects: map[string]store.OwnerReworkSpec{}}
	done := make(chan struct{})
	var workers sync.WaitGroup
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				req, err := ipc.Read(conn)
				if err != nil {
					return
				}
				if req.Kind == ipc.KindReady {
					body := json.RawMessage(`{"status":"ready","epoch":1,"capabilities":["interruption_feedback_v1","owner_request_changes_v2"]}`)
					_ = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindResponse, RequestID: req.RequestID, Epoch: 1, Payload: body})
					return
				}
				if req.Kind == ipc.KindSummary {
					s.mu.Lock()
					source := s.source
					s.summaries++
					s.mu.Unlock()
					summary := store.RunSummary{Version: 1, RunID: "run", Tasks: []store.SummaryTask{{TaskID: source.TaskID, Status: "result_ready", WorkRevision: source.WorkRevision, AllowedActions: []string{"request-changes"}, ReviewableResult: &store.ReviewableResultBinding{EventID: source.EventID, EventRevision: source.EventRevision, EventHash: source.EventHash, ActionSlot: source.ActionSlot, SegmentID: source.SegmentID, CandidateOID: source.CandidateOID, WorkRevision: source.WorkRevision}}}}
					body, _ := json.Marshal(summary)
					_ = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindResponse, RequestID: req.RequestID, Epoch: 1, Payload: body})
					return
				}
				if req.Kind != ipc.KindRequestChanges {
					t.Errorf("replay attempted unexpected source discovery: %s", req.Kind)
					return
				}
				var change coordinator.RequestChangesRequest
				if err = json.Unmarshal(req.Payload, &change); err != nil {
					t.Error(err)
					return
				}
				s.mu.Lock()
				s.requests = append(s.requests, change.OwnerReworkSpec)
				_, seen := s.effects[change.CommandID]
				if !seen {
					s.effects[change.CommandID] = change.OwnerReworkSpec
				}
				first := len(s.requests) == 1
				s.mu.Unlock()
				if first {
					switch mode {
					case "partial":
						_, _ = conn.Write([]byte{0, 0, 0, 64, '{', '"'})
					case "lost":
						return
					}
					<-s.release
					return
				}
				body, _ := json.Marshal(store.ReworkReceipt{Status: "rework_queued", TaskID: change.TaskID, WorkRevision: change.WorkRevision + 1, CandidateOID: change.CandidateOID})
				_ = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindResponse, RequestID: req.RequestID, Epoch: 1, Payload: body})
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); close(s.release); <-done; workers.Wait(); _ = os.RemoveAll(root) })
	return s
}
func ownerMutationRequestFixture(t *testing.T, s *ownerMutationTransport) (string, string, string, store.OwnerReworkSpec) {
	t.Helper()
	root := filepath.Dir(s.state)
	b := hostBootstrap{Version: 2, SocketPath: filepath.Join(s.state, "coordinator.sock"), SpoolRoot: filepath.Join(s.state, "spool"), ProducerID: "launch", Hello: contract.HostHello{LaunchID: "launch", LaunchToken: "token"}}
	bootstrap, err := writeBootstrap(s.state, b)
	if err != nil {
		t.Fatal(err)
	}
	control, err := writeControlCapability(s.state, "run", "owner", "control", bootstrap, "launch", "token")
	if err != nil {
		t.Fatal(err)
	}
	handle := filepath.Join(root, "handle.json")
	if err = writeExclusiveJSON(handle, taskHandle{Version: 1, RunID: "run", StateDir: s.state, TaskIDs: []string{"task"}, ControlFile: control}); err != nil {
		t.Fatal(err)
	}
	spec := store.OwnerReworkSpec{TaskID: "task", WorkRevision: 1, CandidateOID: strings.Repeat("b", 40), EventID: "event", EventRevision: 1, EventHash: strings.Repeat("a", 64), ActionSlot: "slot", SegmentID: "segment", Feedback: "fix", Acceptance: "test", CommandID: "command"}
	s.mu.Lock()
	s.source = spec
	s.mu.Unlock()
	brief := ownerChangeBrief{TaskID: "task", WorkRevision: 1, CandidateOID: spec.CandidateOID, Feedback: spec.Feedback, Acceptance: spec.Acceptance, CommandID: spec.CommandID}
	private := filepath.Join(root, "brief.json")
	if err = writeExclusiveJSON(private, brief); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "owner-decisions")
	if err = os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(directory, dispatchHash("run\x00task\x00command")+".json")
	if err = writeExclusiveJSON(checkpoint, ownerChangeCheckpoint{Version: 1, RunID: "run", StateDir: s.state, ControlFile: control, Brief: brief, Spec: spec}); err != nil {
		t.Fatal(err)
	}
	low := filepath.Join(root, "full.json")
	if err = writeExclusiveJSON(low, struct {
		store.OwnerReworkSpec
		ControlFile string `json:"control_file"`
	}{spec, control}); err != nil {
		t.Fatal(err)
	}
	return handle, private, low, spec
}

func TestOwnerChangeRPCDeadlineRetainsCheckpointAndReplaysExactMutation(t *testing.T) {
	for _, entry := range []string{"high", "low"} {
		for _, mode := range []string{"silent", "partial", "lost"} {
			t.Run(entry+"/"+mode, func(t *testing.T) {
				s := newOwnerMutationTransport(t, mode)
				handle, brief, full, spec := ownerMutationRequestFixture(t, s)
				checkpoint := filepath.Join(filepath.Dir(s.state), "owner-decisions", dispatchHash("run\x00task\x00command")+".json")
				before, err := os.ReadFile(checkpoint)
				if err != nil {
					t.Fatal(err)
				}
				fullBefore, err := os.ReadFile(full)
				if err != nil {
					t.Fatal(err)
				}
				if entry == "high" {
					if err = os.Remove(checkpoint); err != nil {
						t.Fatal(err)
					}
				}
				call := func(ctx context.Context, out io.Writer) error {
					if entry == "high" {
						return taskRequestChanges(ctx, []string{"--handle", handle, "--request", brief}, out)
					}
					return requestChanges(ctx, []string{"--state-dir", s.state, "--request", full}, out)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
				defer cancel()
				start := time.Now()
				err = call(ctx, io.Discard)
				if err == nil || err.Error() != "request_changes_uncertain" || time.Since(start) > time.Second {
					t.Fatalf("unbounded/definite result after lost response: %v elapsed=%s", err, time.Since(start))
				}
				after, err := os.ReadFile(checkpoint)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("lost durable source checkpoint: %v", err)
				}
				fullAfter, err := os.ReadFile(full)
				if err != nil || !bytes.Equal(fullBefore, fullAfter) {
					t.Fatalf("lost durable low-level request: %v", err)
				}
				var replay bytes.Buffer
				if err = call(context.Background(), &replay); err != nil {
					t.Fatal(err)
				}
				var receipt store.ReworkReceipt
				if err = json.Unmarshal(replay.Bytes(), &receipt); err != nil || receipt.WorkRevision != 2 {
					t.Fatalf("replay receipt %s %v", replay.Bytes(), err)
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				if (entry == "high" && s.summaries != 1) || (entry == "low" && s.summaries != 0) {
					t.Fatalf("replay rediscovered a new source tuple: %d summaries", s.summaries)
				}
				if len(s.requests) != 2 || len(s.effects) != 1 || s.requests[0] != spec || s.requests[1] != spec {
					t.Fatalf("replay changed source or duplicated effect: %+v %+v", s.requests, s.effects)
				}
			})
		}
	}
}
func TestOwnerChangeRPCDefaultDeadlineBoundsSilentCoordinator(t *testing.T) {
	s := newOwnerMutationTransport(t, "silent")
	handle, brief, _, _ := ownerMutationRequestFixture(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchdog := time.AfterFunc(6500*time.Millisecond, cancel)
	defer watchdog.Stop()
	start := time.Now()
	err := taskRequestChanges(ctx, []string{"--handle", handle, "--request", brief}, io.Discard)
	elapsed := time.Since(start)
	if err == nil || err.Error() != "request_changes_uncertain" || elapsed < 4*time.Second || elapsed > 6*time.Second {
		t.Fatalf("default mutation deadline: err=%v elapsed=%s", err, elapsed)
	}
}
