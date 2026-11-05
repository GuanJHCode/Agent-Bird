package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
)

func TestTaskRequestChangesV3BoundedIPCAndLostCommitReplay(t *testing.T) {
	root := privateTaskTemp(t)
	state := filepath.Join(root, "state")
	if e := os.Mkdir(state, 0700); e != nil {
		t.Fatal(e)
	}
	listener, e := net.Listen("unix", filepath.Join(state, "coordinator.sock"))
	if e != nil {
		t.Fatal(e)
	}
	var mu sync.Mutex
	var uploaded []byte
	var bodyHash string
	committed := false
	lost := false
	summaries := 0
	chunks := 0
	var failures []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			func() {
				defer conn.Close()
				m, e := ipc.Read(conn)
				if e != nil {
					mu.Lock()
					failures = append(failures, e.Error())
					mu.Unlock()
					return
				}
				mu.Lock()
				defer mu.Unlock()
				var payload any
				switch m.Kind {
				case ipc.KindReady:
					payload = map[string]any{"status": "ready", "epoch": 1, "capabilities": []string{"interruption_feedback_v1", "owner_request_changes_v2", "owner_rework_refs_v3"}}
				case ipc.KindSummary:
					summaries++
					payload = store.RunSummary{Version: 1, RunID: "run", Tasks: []store.SummaryTask{{TaskID: "task", Status: "result_ready", WorkRevision: 1, AllowedActions: []string{"request-changes"}, ReviewableResult: &store.ReviewableResultBinding{EventID: "event", EventRevision: 1, EventHash: strings.Repeat("b", 64), ActionSlot: "slot", SegmentID: "segment", CandidateOID: strings.Repeat("a", 40), WorkRevision: 1}}}}
				case ipc.KindRequestChanges:
					if len(m.Payload) > 32*1024 {
						failures = append(failures, "large IPC payload")
					}
					var q coordinator.RequestChangesRequest
					if e = json.Unmarshal(m.Payload, &q); e != nil || q.RefsV3 == nil || q.Feedback != "" || q.Acceptance != "" {
						failures = append(failures, "inline body or invalid request")
						return
					}
					r := q.RefsV3
					if bodyHash != "" && bodyHash != r.BodySHA256 {
						failures = append(failures, "changed hash")
					}
					bodyHash = r.BodySHA256
					receipt := store.ReworkReceipt{Status: "rework_queued", TaskID: "task", WorkRevision: 2, CandidateOID: q.CandidateOID}
					if committed {
						payload = store.ReworkUploadReceipt{Status: "rework_queued", Receipt: &receipt}
						break
					}
					switch r.Phase {
					case "begin":
						payload = store.ReworkUploadReceipt{Status: "uploading", UploadID: "upload", WrittenBytes: int64(len(uploaded))}
					case "chunk":
						chunks++
						if r.Offset != int64(len(uploaded)) || len(r.Data) > adapter.ReworkChunkMaxBytes {
							failures = append(failures, "invalid bounded chunk")
							return
						}
						uploaded = append(uploaded, r.Data...)
						payload = store.ReworkUploadReceipt{Status: "uploading", UploadID: "upload", WrittenBytes: int64(len(uploaded))}
					case "commit":
						committed = true
						lost = true
						return
					default:
						failures = append(failures, "unexpected phase")
						return
					}
				default:
					failures = append(failures, "unexpected message")
					return
				}
				raw, _ := json.Marshal(payload)
				ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindResponse, RequestID: m.RequestID, Epoch: 1, Payload: raw})
			}()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	bootstrap := hostBootstrap{Version: 2, ProducerID: "launch", SocketPath: filepath.Join(state, "coordinator.sock"), SpoolRoot: filepath.Join(state, "spool"), Hello: contract.HostHello{LaunchID: "launch", LaunchToken: "token"}}
	bp, e := writeBootstrap(state, bootstrap)
	if e != nil {
		t.Fatal(e)
	}
	control, e := writeControlCapability(state, "run", "owner", "token", bp, "launch", "token")
	if e != nil {
		t.Fatal(e)
	}
	handle, path := filepath.Join(root, "handle.json"), filepath.Join(root, "request.json")
	if e = writeExclusiveJSON(handle, taskHandle{Version: 1, RunID: "run", StateDir: state, ControlFile: control, TaskIDs: []string{"task"}}); e != nil {
		t.Fatal(e)
	}
	brief := ownerChangeBrief{Version: 3, TaskID: "task", WorkRevision: 1, CandidateOID: strings.Repeat("a", 40), Feedback: strings.Repeat("反馈<\"\n", 12000), Acceptance: "all original acceptance", CommandID: "command"}
	if e = writeExclusiveJSON(path, brief); e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	args := []string{"--handle", handle, "--request", path}
	if e = taskRequestChanges(context.Background(), args, &out); e == nil || e.Error() != "request_changes_uncertain" {
		t.Fatalf("lost reply %v", e)
	}
	if e = taskRequestChanges(context.Background(), args, &out); e != nil {
		t.Fatal(e)
	}
	mu.Lock()
	defer mu.Unlock()
	if !lost || !committed || summaries != 1 || chunks < 5 || len(failures) != 0 {
		t.Fatalf("transport lost=%v commit=%v summaries=%d chunks=%d failures=%v", lost, committed, summaries, chunks, failures)
	}
	if dispatchHash(string(uploaded)) != bodyHash {
		t.Fatal("body changed in transport")
	}
	body, e := adapter.DecodeReworkBody(uploaded)
	if e != nil || body.Feedback != brief.Feedback || body.Acceptance != brief.Acceptance {
		t.Fatalf("body incomplete %v", e)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "owner-decisions"))
	if len(entries) != 1 {
		t.Fatalf("checkpoints %d", len(entries))
	}
	checkpoint, _ := os.ReadFile(filepath.Join(root, "owner-decisions", entries[0].Name()))
	if bytes.Contains(checkpoint, []byte("all original acceptance")) {
		t.Fatal("feedback duplicated in checkpoint")
	}
}
