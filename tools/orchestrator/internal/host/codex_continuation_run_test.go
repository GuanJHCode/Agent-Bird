package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Real M1/M3 + durable Host consumer, synthetic native frames/process identity.
// No process, OS exit capability or original-exit publisher is supplied here.
func codexRunContinuationPipe(t *testing.T, h *Host, p codexPreparedState, g contract.LaunchCommand, mutation string) (codexsession.Outcome, codexBoundThread, error) {
	t.Helper()
	id := process.Identity{PID: 2345 + g.QuestionRevision, PGID: 2345 + g.QuestionRevision, Birth: fmt.Sprintf("synthetic-%d", g.QuestionRevision), BirthKnown: true, Executable: p.Launch.Path, ExecutableSHA256: p.Launch.SHA256}
	spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	if err != nil {
		t.Fatal(err)
	}
	meta := launchMetadata{commandID: g.CommandID, executionEpoch: g.ExecutionEpoch, workRevision: g.WorkRevision}
	for _, kind := range []string{contract.EventSpawned, contract.EventRunning} {
		if _, err := h.appendPhaseEvent(spool, g.RunID, g.TaskID, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, meta, kind, hashText(kind), &id, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	local, remote := net.Pipe()
	client, err := codexrpc.New(ctx, local, codexrpc.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		defer remote.Close()
		dec, enc := json.NewDecoder(remote), json.NewEncoder(remote)
		b := p.Previous.Thread.Binding.native()
		for _, method := range []string{"initialize", "initialized", "config/read", "thread/resume", "turn/start"} {
			var frame map[string]json.RawMessage
			if err := dec.Decode(&frame); err != nil {
				if mutation != "" && method == "turn/start" && err == io.EOF {
					done <- nil
				} else {
					done <- errors.New("resume phase missing")
				}
				return
			}
			var actual string
			_ = json.Unmarshal(frame["method"], &actual)
			if actual != method {
				done <- errors.New("resume used thread/start or skipped phase")
				return
			}
			if method == "initialized" {
				continue
			}
			var result map[string]any
			switch method {
			case "initialize":
				result = map[string]any{"codexHome": p.Layout.Home.Path, "platformFamily": "unix", "platformOs": "macos", "userAgent": "pure-fixture"}
			case "config/read":
				result = map[string]any{"config": codexThreadEffectiveFixture(p), "origins": map[string]any{}}
			case "thread/resume":
				var params map[string]any
				if json.Unmarshal(frame["params"], &params) != nil || params["threadId"] != b.ThreadID || params["excludeTurns"] != true {
					done <- errors.New("wrong exact resume selector")
					return
				}
				// Native startup legitimately changes runtime bytes. The callback
				// must verify immutable ancestors, not reuse the old physical seal.
				if err := os.WriteFile(filepath.Join(p.Layout.SQLite.Path, "state_5.sqlite-wal"), []byte("synthetic new native runtime bytes"), 0644); err != nil {
					done <- err
					return
				}
				if mutation == "path" {
					b.RolloutPath += ".other"
				}
				if mutation == "thread" {
					b.ThreadID = "22345678-1234-4321-8123-123456789abc"
				}
				thread := map[string]any{"id": b.ThreadID, "preview": "", "ephemeral": false, "modelProvider": "openai", "createdAt": 1, "updatedAt": 1, "status": map[string]any{"type": "idle"}, "path": b.RolloutPath, "historyMode": b.HistoryMode, "cwd": p.Origin.Workspace, "cliVersion": "0.154.0", "source": "appServer", "sessionId": "synthetic-session", "projectId": nil, "turns": []any{}}
				result = map[string]any{"approvalPolicy": "on-request", "approvalsReviewer": "user", "cwd": p.Origin.Workspace, "model": b.Model, "modelProvider": "openai", "reasoningEffort": "source-effort", "serviceTier": nil, "sandbox": map[string]any{"type": "readOnly", "networkAccess": false}, "thread": thread}
			case "turn/start":
				events, err := spool.Read()
				if err != nil || len(events) != 3 || events[2].Kind != contract.EventSession || events[2].SessionID != b.ThreadID {
					done <- errors.New("resume turn preceded durable Session")
					return
				}
				result = map[string]any{"turn": map[string]any{"id": "turn-resume", "items": []any{}, "status": "inProgress", "startedAt": 2}}
			}
			if err := enc.Encode(map[string]any{"id": frame["id"], "result": result}); err != nil {
				done <- err
				return
			}
		}
		item := map[string]any{"id": "final", "type": "agentMessage", "phase": "final_answer", "text": "{\"version\":1,\"kind\":\"technical_question\",\"text\":\"Which next file?\"}"}
		if err := enc.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": b.ThreadID, "turn": map[string]any{"id": "turn-resume", "status": "completed", "items": []any{item}, "startedAt": 2, "completedAt": 3, "error": nil}}}); err != nil {
			done <- err
			return
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			done <- errors.New("transport not closed/joined")
			return
		}
		done <- nil
	}()
	out, bound, runErr := h.runManagedCodexTurn(ctx, client, p, g, id)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.Done():
	default:
		t.Fatal("M1 jobs not joined")
	}
	return out, bound, runErr
}

func codexFinishSyntheticQuestion(t *testing.T, h *Host, p codexPreparedState, g contract.LaunchCommand, bound codexBoundThread, text string) (contract.LaunchCommand, adapter.Invocation) {
	t.Helper()
	candidate, err := h.captureManagedCodexQuestionSeal(context.Background(), p, g, bound, g.QuestionRevision+1, hashText(text), codexStateReadControl{}, codexSealControl{})
	if err != nil {
		t.Fatal("resumed seal capture", err)
	}
	body, err := codexEncodeSeal(candidate.Record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codexWriteProtectedRecord(context.Background(), candidate.Protected, "codex-session-seal.json", body); err != nil {
		t.Fatal(err)
	}
	spool, _ := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	current := bound.Current
	id := process.Identity{PID: current.Process.PID, PGID: current.Process.PGID, Birth: current.Process.Birth, BirthKnown: true, Executable: current.Executable, ExecutableSHA256: current.ExecutableSHA256}
	meta := launchMetadata{commandID: g.CommandID, executionEpoch: g.ExecutionEpoch, workRevision: g.WorkRevision}
	zero := 0
	question, err := h.appendStructuredEvent(spool, g.RunID, g.TaskID, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, meta, contract.EventQuestion, hashText(text), &id, &zero, nil, structuredEventFields{SessionKind: "session-id", SessionID: bound.Record.Binding.ThreadID, QuestionRevision: g.QuestionRevision + 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.appendStructuredEvent(spool, g.RunID, g.TaskID, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, meta, contract.EventExited, hashText("exited"), &id, &zero, nil, structuredEventFields{ActiveMS: 10}); err != nil {
		t.Fatal(err)
	}
	ref := &contract.ContinuationRef{Version: 1, Mode: contract.ContinuationQuestion, SourceKind: "host", SourceSegmentID: g.SegmentID, SourceEventID: question.EventID, SourceEventRevision: question.Sequence, SourceEventSHA256: question.PayloadHash, SessionEventID: bound.InitialSession.EventID, AdapterPayloadSHA256: codexDigest(g.AdapterPayload), OwnerContextID: "owner"}
	g.SegmentID, g.CommandID, g.ExecutionEpoch = fmt.Sprintf("segment-%d", g.QuestionRevision+2), fmt.Sprintf("command-%d", g.QuestionRevision+2), g.ExecutionEpoch+1
	g.SessionKind, g.SessionID = "session-id", bound.Record.Binding.ThreadID
	g.QuestionID, g.QuestionRevision, g.Answer, g.Continuation = question.QuestionID, g.QuestionRevision+1, "Read the next authorized file.", ref
	inv, err := adapter.BuildManagedCodexInvocation(adapter.Request{Provider: adapter.ProviderCodex, Binary: p.Origin.Lock.Binary, Lock: &p.Origin.Lock, Profile: &p.Origin.Profile, CWD: p.Origin.Workspace, Prompt: g.Answer, Session: adapter.SessionRef{Kind: adapter.SessionID, ID: g.SessionID}, Continuation: ref})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}); err != nil {
		t.Fatal(err)
	}
	return g, inv
}

func TestCodexContinuationActualM3ResumeTwoAnswersKeepsOriginalBinding(t *testing.T) {
	h, original, g, inv, env := codexContinuationFixture(t)
	for cycle := 0; cycle < 2; cycle++ {
		t.Log("cold-cycle", cycle)
		var err error
		h, err = NewIPC(h.spoolRoot, h.producerID)
		if err != nil || h.BindContinuationOwner("owner") != nil {
			t.Fatal("cold Host owner binding")
		}
		p, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
		if err != nil {
			sourceRoot, rootErr := codexAnchorStateRoot(context.Background(), filepath.Join(h.spoolRoot, g.AttemptID, g.Continuation.SourceSegmentID))
			var r codexSealRecord
			file, readErr := codexReadProtectedRecord(context.Background(), sourceRoot, "codex-session-seal.json", &r)
			var thread codexThreadRecord
			threadFile, threadErr := codexReadProtectedRecord(context.Background(), original.Protected, "codex-thread.json", &thread)
			diagnostic := original
			diagnostic.Previous = &codexContinuationState{Thread: thread, ThreadFile: threadFile}
			validErr := h.codexValidateSealedRecord(context.Background(), diagnostic, sourceRoot, r)
			_, _, _, eventErr := h.codexSealedSegmentEvents(diagnostic, r)
			remaining := codexStateTotalLimit
			physical, physicalErr := codexCapturePhysicalState(context.Background(), original.Layout, thread.Binding.native(), original.Origin.InstallationID, codexStateReadControl{Remaining: &remaining})
			t.Log("safe-stage-booleans", rootErr == nil, readErr == nil, threadErr == nil, validErr == nil, eventErr == nil, physicalErr == nil, reflect.DeepEqual(physical, r.State), file.Identity.Size <= 16<<10)
			t.Fatal("cold restore", err)
		}
		out, bound, err := codexRunContinuationPipe(t, h, p, g, "")
		if err != nil || out.Completion.Kind != "technical_question" || bound.Session.EventID == bound.InitialSession.EventID || bound.InitialSession.EventID != g.Continuation.SessionEventID || bound.Record.First.SegmentID != original.Origin.Scope.InitialSegmentID || bound.Current.SegmentID != g.SegmentID || h.codexVerifyBoundThread(context.Background(), p, bound) != nil {
			t.Fatal("resume consumer did not bind original/new Session", err)
		}
		if p.OriginFile != original.OriginFile || !reflect.DeepEqual(p.Layout, original.Layout) {
			t.Fatal("fresh origin/runtime allocated")
		}
		var actual codexThreadRecord
		file, err := codexReadProtectedRecord(context.Background(), original.Protected, "codex-thread.json", &actual)
		if err != nil || file != bound.File || actual != bound.Record {
			t.Fatal("original binding replaced")
		}
		g, inv = codexFinishSyntheticQuestion(t, h, p, g, bound, out.Completion.Text)
	}
	p, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
	if err != nil || len(p.Previous.Segments) != 3 || p.Previous.Segments[0].Record.QuestionRevision != 3 || p.Previous.Segments[2].Record.Source.SegmentID != original.Origin.Scope.InitialSegmentID {
		t.Fatal("three-segment sealed chain not linked", err)
	}
}

func TestCodexContinuationNativeBindingDriftHasNoNewSessionOrTurn(t *testing.T) {
	for _, kind := range []string{"path", "thread"} {
		t.Run(kind, func(t *testing.T) {
			h, _, g, inv, env := codexContinuationFixture(t)
			p, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
			if err != nil {
				t.Fatal(err)
			}
			out, bound, err := codexRunContinuationPipe(t, h, p, g, kind)
			if err == nil || out.Completion.Kind != "" || bound.Session.EventID != "" {
				t.Fatal("drift authorized a new turn")
			}
			spool, _ := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
			events, err := spool.Read()
			if err != nil || len(events) != 2 {
				t.Fatal("drift appended Session/terminal")
			}
		})
	}
}
