package host

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestInterruptedProviderPublishesReasonAndIncompleteCheckpoint(t *testing.T) {
	if os.Getenv("CBO_PROGRESS_FIXTURE") == "1" {
		signal.Ignore(syscall.SIGTERM)
		fmt.Println(`{"type":"system","subtype":"init","session_id":"session-1"}`)
		fmt.Println(`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"PRIVATE_THINKING"},{"type":"text","text":"Partial public finding"},{"type":"tool_use","name":"Read","input":{"secret":"PRIVATE_ARGUMENT"}}]}}`)
		fmt.Println(`{"type":"user","message":{"content":[{"type":"text","text":"PRIVATE_USER"}]}}`)
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	root := t.TempDir()
	h, err := NewIPC(root, "producer")
	if err != nil {
		t.Fatal(err)
	}
	var progressEvents []contract.Event
	h.progressSink = func(_ context.Context, e contract.Event) error {
		progressEvents = append(progressEvents, e)
		return nil
	}
	bin, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a := store.Attempt{ID: "a", TaskID: "task", SegmentID: "s"}
	result, err := h.execute(ctx, a, "run", "task", process.Command{Path: bin, Args: []string{"-test.run=^TestInterruptedProviderPublishesReasonAndIncompleteCheckpoint$"}, Env: []string{"CBO_PROGRESS_FIXTURE=1"}}, false, launchMetadata{commandID: "c", workRevision: 1, outputProvider: "claude-code"})
	if !errors.Is(err, context.DeadlineExceeded) || result.Status != "interrupted" || result.ArtifactPath != "" {
		t.Fatalf("%+v %v", result, err)
	}
	got, err := h.Collect(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, progressEvents...)
	stopped, checkpoint := false, false
	for _, e := range got {
		if e.Kind == contract.EventResult {
			t.Fatal("partial accepted as final")
		}
		if e.Kind == contract.EventStopped {
			stopped = true
			b, _ := json.Marshal(e)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			if m["stop_reason"] != "deadline_exceeded" || e.Artifact != nil {
				t.Fatalf("missing reason or changed retry contract: %s", b)
			}
		}
		if e.Kind == "progress" {
			if e.Artifact == nil {
				t.Fatal("unregistered checkpoint")
			}
			b, err := os.ReadFile(e.Artifact.Path)
			if err != nil {
				t.Fatal(err)
			}
			if e.Artifact.SHA256 != hashBytes(b) || int64(len(b)) != e.Artifact.Size {
				t.Fatal("bad checkpoint identity")
			}
			if strings.Contains(string(b), "PRIVATE_") {
				t.Fatalf("private content in checkpoint: %s", b)
			}
			var m map[string]any
			if json.Unmarshal(b, &m) != nil {
				t.Fatal("invalid checkpoint")
			}
			if m["status"] != "incomplete" {
				t.Fatal("not marked incomplete")
			}
			if m["assistant_text"] == "Partial public finding" {
				checkpoint = true
			}
		}
	}
	if !stopped || !checkpoint {
		t.Fatalf("missing stop/checkpoint: %v %v", stopped, checkpoint)
	}
}

type interruptionInvocation struct {
	launchInvocation
	profile *adapter.ExecutionProfile
}

func (i interruptionInvocation) ExecutionProfile() *adapter.ExecutionProfile { return i.profile }
func (i interruptionInvocation) OutputProvider() string                      { return "claude-code" }

func TestLaunchInterruptionPreservesDeadlineSource(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		budget, profile, deadline int64
		reason                    string
	}{
		{"budget", 1000, 5000, 10000, "active_budget_exhausted"},
		{"profile", 5000, 1000, 10000, "profile_timeout"},
		{"task", 5000, 5000, 1000, "task_deadline_exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			h, err := NewIPC(root, "producer")
			if err != nil {
				t.Fatal(err)
			}
			bin, err := filepath.EvalSymlinks(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			inv := interruptionInvocation{launchInvocation: launchInvocation{args: []string{bin, "-test.run=^TestInterruptedProviderPublishesReasonAndIncompleteCheckpoint$"}, dir: root, env: map[string]string{"CBO_PROGRESS_FIXTURE": "1"}}, profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: tc.profile}}
			g := contract.LaunchCommand{ReservationID: "reservation", CommandID: "c", AttemptID: "a", SegmentID: "s", RunID: "r", TaskID: "t", GrantedActiveMS: tc.budget, DeadlineUnixMS: time.Now().Add(time.Duration(tc.deadline) * time.Millisecond).UnixMilli(), WorkRevision: 1}
			result, err := h.ExecuteLaunch(context.Background(), g, inv)
			if !errors.Is(err, context.DeadlineExceeded) || result.Status != "interrupted" {
				t.Fatalf("%+v %v", result, err)
			}
			ev, err := h.Collect(context.Background(), store.Attempt{ID: "a", SegmentID: "s"})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, e := range ev {
				if e.Kind == contract.EventStopped {
					found = true
					if e.StopReason != tc.reason {
						t.Fatalf("reason=%s", e.StopReason)
					}
				}
			}
			if !found {
				t.Fatal("missing stopped")
			}
		})
	}
}

func TestProgressSnapshotBoundedAndDoesNotFinalizeTail(t *testing.T) {
	p, err := newProtocolCollector("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("中", 4000)
	message, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}})
	_, _ = p.Write(append(message, '\n'))
	_, _ = p.Write([]byte(`{"type":"result","subtype":"success","result":"SECRET_PENDING"}`))
	s := p.progressSnapshot()
	if !s.Available || s.Status != "incomplete" || !s.TextTruncated || len(s.AssistantText) > 8192 || s.CompleteLines != 1 || strings.Contains(s.AssistantText, "SECRET") {
		t.Fatalf("bad snapshot %+v", s)
	}
	if !p.Snapshot().TailPending || p.Snapshot().TerminalSeen {
		t.Fatal("snapshot finalized the stream")
	}
}
