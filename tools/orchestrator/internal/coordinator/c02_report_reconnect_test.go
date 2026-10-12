package coordinator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type c02ReportWorkerInvocation struct {
	args []string
	env  map[string]string
}

func (i c02ReportWorkerInvocation) Args() []string                 { return i.args }
func (i c02ReportWorkerInvocation) WorkingDirectory() string       { return "" }
func (i c02ReportWorkerInvocation) Stdin() []byte                  { return nil }
func (i c02ReportWorkerInvocation) Environment() map[string]string { return i.env }

type c02WorkerReportCapability struct {
	Version      int    `json:"version"`
	SocketPath   string `json:"socket_path"`
	CapabilityID string `json:"capability_id"`
	Token        string `json:"token"`
}
type c02WorkerReportOutcome struct {
	Error          string `json:"error"`
	CapabilityPath string `json:"capability_path"`
	TokenSHA256    string `json:"token_sha256"`
	PID            int    `json:"pid"`
}

func TestC02ReportWorkerHelper(t *testing.T) {
	if os.Getenv("C02_REPORT_WORKER") != "1" {
		return
	}
	outcome := c02WorkerReportOutcome{CapabilityPath: os.Getenv("ORCHESTRATOR_REPORT_CAPABILITY"), PID: os.Getpid()}
	reportErr := func() error {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(os.Getenv("C02_REPORT_RELEASE")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		body, err := os.ReadFile(outcome.CapabilityPath)
		if err != nil {
			return err
		}
		var cap c02WorkerReportCapability
		if err := json.Unmarshal(body, &cap); err != nil {
			return err
		}
		tokenHash := sha256.Sum256([]byte(cap.Token))
		outcome.TokenSHA256 = hex.EncodeToString(tokenHash[:])
		artifact := filepath.Join(filepath.Dir(outcome.CapabilityPath), "worker-result.txt")
		content := []byte("original-worker-result\n")
		if err := os.WriteFile(artifact, content, 0600); err != nil {
			return err
		}
		hash := sha256.Sum256(content)
		question, _ := json.Marshal(map[string]any{"status": "waiting_user", "question_id": "worker-question", "question_revision": 1, "question_kind": "technical", "artifact": map[string]any{"id": "question", "path": artifact, "size": len(content), "sha256": hex.EncodeToString(hash[:])}})
		result, _ := json.Marshal(map[string]any{"status": "completed", "artifact": map[string]any{"id": "worker-result", "path": artifact, "size": len(content), "sha256": hex.EncodeToString(hash[:])}})
		for i, payload := range []json.RawMessage{question, result} {
			kind := "question"
			if i == 1 {
				kind = "result"
			}
			raw, _ := json.Marshal(ReportEventRequest{CapabilityID: cap.CapabilityID, Token: cap.Token, EventID: "worker-" + kind, Sequence: int64(i + 1), Kind: kind, Payload: payload})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			response, err := ipc.Call(ctx, cap.SocketPath, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindReportEvent, RequestID: "report-" + kind, Payload: raw})
			cancel()
			if err != nil {
				return err
			}
			if response.Kind == ipc.KindError {
				var value ErrorResponse
				_ = json.Unmarshal(response.Payload, &value)
				return errors.New(value.Error)
			}
			var ack contract.DurableAck
			if response.Kind != ipc.KindResponse || json.Unmarshal(response.Payload, &ack) != nil || ack.Status != "durable" {
				return errors.New("report_not_durable")
			}
		}
		return nil
	}()
	if reportErr != nil {
		outcome.Error = reportErr.Error()
	}
	body, _ := json.Marshal(outcome)
	if err := os.WriteFile(os.Getenv("C02_REPORT_OUTCOME"), body, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestC02ReportCapabilityReconnectKeepsOriginalLiveWorker(t *testing.T) {
	root, err := os.MkdirTemp("/private/tmp", "c02-report-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	state := filepath.Join(root, "state")
	first, err := NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- first.Serve(firstCtx) }()
	t.Cleanup(func() { _ = first.Close(); cancelFirst() })
	pid, birth, executable := testHostIdentity(t)
	plan, err := first.db.SubmitPlan(context.Background(), store.PlanSpec{Run: store.RunSpec{ID: "report-run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: pid, OriginBirth: birth}, Host: store.HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: executable}, Tasks: []store.TaskSpec{{ID: "report-task", RunID: "report-run", MaxAttempts: 1, MaxActiveMS: 15000, CompletionPolicy: "exit_success_fixture", AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	barrier := c02StartWireBarrier(t, root, first.SocketPath(), true)
	spool := filepath.Join(root, "spool")
	release := filepath.Join(root, "release")
	outcomePath := filepath.Join(root, "outcome.json")
	grants := make(chan contract.LaunchCommand, 1)
	var invocations atomic.Int32
	hostCtx, cancelHost := context.WithTimeout(context.Background(), 15*time.Second)
	hostDone := make(chan error, 1)
	go func() {
		hostDone <- host.RunSource(hostCtx, host.SourceConfig{SocketPath: barrier.socket, SpoolRoot: spool, Hello: contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: "origin", OriginPID: pid, OriginBirth: birth, HostGeneration: "generation", PID: pid, Birth: birth, Executable: executable}, InvocationProvider: func(_ context.Context, grant contract.LaunchCommand) (contract.InvocationView, error) {
			invocations.Add(1)
			grants <- grant
			return c02ReportWorkerInvocation{args: []string{executable, "-test.run=^TestC02ReportWorkerHelper$"}, env: map[string]string{"C02_REPORT_WORKER": "1", "C02_REPORT_RELEASE": release, "C02_REPORT_OUTCOME": outcomePath}}, nil
		}})
	}()
	t.Cleanup(func() {
		cancelHost()
		select {
		case <-hostDone:
		case <-time.After(13 * time.Second):
			t.Error("Source cleanup did not join")
		}
	})
	var capID, tokenHash, capDir string
	var workerPID int
	c02Wait(t, "registered capability and original live worker", func() bool {
		return first.db.SQL().QueryRow(`SELECT c.capability_id,c.token_hash,c.capability_dir,json_extract(e.body_json,'$.process.pid') FROM report_capabilities c JOIN runtime_events e ON e.segment_id=c.segment_id WHERE c.status='registered' AND json_extract(e.body_json,'$.kind')='running'`).Scan(&capID, &tokenHash, &capDir, &workerPID) == nil
	})
	originalGrant := <-grants
	first.mu.Lock()
	firstSession := first.hosts[plan.LaunchID]
	first.mu.Unlock()
	if err := sendLaunch(firstSession, first.Epoch(), originalGrant); err != nil {
		t.Fatal(err)
	}
	workerBirth, err := process.Birth(workerPID)
	if err != nil {
		t.Fatal(err)
	}
	capabilityPath := filepath.Join(capDir, "capability.json")
	originalCapability, err := os.ReadFile(capabilityPath)
	if err != nil {
		t.Fatal(err)
	}
	oldEpoch := first.Epoch()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	cancelFirst()
	<-firstDone
	second, err := NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	go func() { _ = second.Serve(secondCtx) }()
	t.Cleanup(func() { _ = second.Close(); cancelSecond() })
	c02Wait(t, "same Host ready in new epoch", func() bool {
		var epoch uint64
		var status string
		return second.db.SQL().QueryRow(`SELECT coordinator_epoch,status FROM runtime_hosts WHERE id=?`, plan.LaunchID).Scan(&epoch, &status) == nil && epoch == second.Epoch() && status == "ready"
	})
	if second.Epoch() <= oldEpoch {
		t.Fatal("epoch not advanced")
	}
	if got, err := process.Birth(workerPID); err != nil || got != workerBirth {
		t.Fatalf("worker changed across reconnect: %q %v", got, err)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	c02Wait(t, "original worker report outcome", func() bool { _, err := os.Stat(outcomePath); return err == nil })
	var outcome c02WorkerReportOutcome
	body, err := os.ReadFile(outcomePath)
	if err != nil || json.Unmarshal(body, &outcome) != nil {
		t.Fatalf("outcome: %v", err)
	}
	if outcome.Error != "" {
		t.Fatalf("original live worker report rejected after epoch change: %s", outcome.Error)
	}
	if outcome.PID != workerPID || outcome.CapabilityPath != capabilityPath || outcome.TokenSHA256 != tokenHash || invocations.Load() != 1 {
		t.Fatalf("report binding changed: pid=%d want=%d path=%s invocations=%d", outcome.PID, workerPID, outcome.CapabilityPath, invocations.Load())
	}
	currentCapability, err := os.ReadFile(capabilityPath)
	if err != nil || string(currentCapability) != string(originalCapability) {
		t.Fatalf("worker capability bytes changed: %v", err)
	}
	var question, result, attempts, segments, budgets int
	if err := second.db.SQL().QueryRow(`SELECT (SELECT COUNT(*) FROM report_events WHERE kind='question'),(SELECT COUNT(*) FROM report_events WHERE kind='result'),(SELECT COUNT(*) FROM attempts),(SELECT COUNT(*) FROM segments),(SELECT COUNT(*) FROM budget_runtime)`).Scan(&question, &result, &attempts, &segments, &budgets); err != nil || question != 1 || result != 1 || attempts != 1 || segments != 1 || budgets != 1 {
		t.Fatalf("report/ledger counts=%d/%d/%d/%d/%d err=%v", question, result, attempts, segments, budgets, err)
	}
	select {
	case <-barrier.lost:
	case <-time.After(3 * time.Second):
		t.Fatal("final Exited ACK-loss barrier not reached")
	}
	segmentDir := filepath.Join(spool, originalGrant.AttemptID, originalGrant.SegmentID)
	files, err := filepath.Glob(filepath.Join(segmentDir, "event-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var exitedPath string
	var exitedBytes []byte
	var exitedSequence int64
	for _, path := range files {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var event contract.Event
		if json.Unmarshal(body, &event) == nil && event.Kind == contract.EventExited {
			exitedPath, exitedBytes, exitedSequence = path, body, event.Sequence
		}
	}
	if exitedPath == "" {
		t.Fatal("lost final ACK did not leave its original Exited spool file")
	}
	var settledBefore int64
	if err := second.db.SQL().QueryRow(`SELECT used_ms FROM budget_runtime WHERE status='settled'`).Scan(&settledBefore); err != nil {
		t.Fatal(err)
	}
	// The real worker has exited and its Exited event committed. Seed an
	// already revoked historical capability to exercise that legal state during
	// the next reconcile; revocation-on-Exited is not a baseline contract.
	if _, err := second.db.SQL().Exec(`UPDATE report_capabilities SET status='revoked' WHERE capability_id=?`, capID); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	cancelSecond()
	third, err := NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	thirdCtx, cancelThird := context.WithCancel(context.Background())
	go func() { _ = third.Serve(thirdCtx) }()
	t.Cleanup(func() { _ = third.Close(); cancelThird() })
	c02Wait(t, "actual Source replay after lost final Exited ACK", func() bool {
		var epoch, capabilityEpoch uint64
		var status string
		if third.db.SQL().QueryRow(`SELECT h.coordinator_epoch,h.status,c.execution_epoch FROM runtime_hosts h JOIN report_capabilities c ON c.producer_id=h.id WHERE h.id=? AND c.capability_id=?`, plan.LaunchID, capID).Scan(&epoch, &status, &capabilityEpoch) != nil || epoch != third.Epoch() || capabilityEpoch != epoch || status != "ready" {
			return false
		}
		// RegisterHost briefly exposes ready before BeginHostReconcile. That
		// projection alone does not prove this Source consumed the durable ACK.
		archived, err := os.ReadFile(filepath.Join(segmentDir, "archive", filepath.Base(exitedPath)))
		published, cursorErr := os.ReadFile(filepath.Join(spool, ".producer-published"))
		cursor, parseErr := strconv.ParseInt(string(bytes.TrimSpace(published)), 10, 64)
		return err == nil && string(archived) == string(exitedBytes) && cursorErr == nil && parseErr == nil && cursor == exitedSequence
	})
	archived, err := os.ReadFile(filepath.Join(segmentDir, "archive", filepath.Base(exitedPath)))
	if err != nil || string(archived) != string(exitedBytes) {
		t.Fatalf("final ACK replay changed original Exited bytes: %v", err)
	}
	published, err := os.ReadFile(filepath.Join(spool, ".producer-published"))
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := strconv.ParseInt(string(bytes.TrimSpace(published)), 10, 64)
	if err != nil || cursor != exitedSequence {
		t.Fatalf("actual Source did not advance original final cursor: %d/%d %v", cursor, exitedSequence, err)
	}
	var exits, unknowns int
	var settledAfter int64
	var capStatus string
	var capEpoch uint64
	if err := third.db.SQL().QueryRow(`SELECT (SELECT COUNT(*) FROM runtime_events WHERE json_extract(body_json,'$.kind')='exited'),(SELECT COUNT(*) FROM runtime_events WHERE json_extract(body_json,'$.kind')='unknown')`).Scan(&exits, &unknowns); err != nil || exits != 1 || unknowns != 0 {
		t.Fatalf("duplicate/running replay changed terminal outcome: exits=%d unknown=%d %v", exits, unknowns, err)
	}
	if err := third.db.SQL().QueryRow(`SELECT used_ms FROM budget_runtime WHERE status='settled'`).Scan(&settledAfter); err != nil || settledAfter != settledBefore {
		t.Fatalf("final ACK replay settled twice/reset budget: %d/%d %v", settledBefore, settledAfter, err)
	}
	if err := third.db.SQL().QueryRow(`SELECT status,execution_epoch FROM report_capabilities WHERE capability_id=?`, capID).Scan(&capStatus, &capEpoch); err != nil || capStatus != "revoked" || capEpoch != third.Epoch() {
		t.Fatalf("completed report binding blocked Host or resurrected: %s epoch=%d %v", capStatus, capEpoch, err)
	}
	third.mu.Lock()
	thirdSession := third.hosts[plan.LaunchID]
	third.mu.Unlock()
	if err := sendLaunch(thirdSession, third.Epoch(), originalGrant); err != nil {
		t.Fatal(err)
	}
	// A fourth transport/Hello barrier establishes processing of the preceding
	// completed duplicate, without sleeps or a new launch/worker.
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
	cancelThird()
	fourth, err := NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	fourthCtx, cancelFourth := context.WithCancel(context.Background())
	go func() { _ = fourth.Serve(fourthCtx) }()
	t.Cleanup(func() { _ = fourth.Close(); cancelFourth() })
	c02Wait(t, "completed duplicate followed by fresh reconcile", func() bool {
		var epoch uint64
		var status string
		return fourth.db.SQL().QueryRow(`SELECT coordinator_epoch,status FROM runtime_hosts WHERE id=?`, plan.LaunchID).Scan(&epoch, &status) == nil && epoch == fourth.Epoch() && status == "ready"
	})
	if invocations.Load() != 1 {
		t.Fatalf("running/completed duplicate invoked provider %d times", invocations.Load())
	}
	if err := fourth.db.SQL().QueryRow(`SELECT COUNT(*) FROM runtime_events WHERE json_extract(body_json,'$.kind')='exited'`).Scan(&exits); err != nil || exits != 1 {
		t.Fatalf("completed duplicate appended another exit: %d %v", exits, err)
	}
	t.Log("same worker_pid=" + strconv.Itoa(workerPID) + " same cap/token/path; durable question1/result1; execution ledger1")
}
