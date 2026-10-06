package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type typedContinuationFixture struct {
	db                                 *DB
	path, host, sessionKind, sessionID string
	payload                            json.RawMessage
	receipt                            SubmitReceipt
	grant                              contract.LaunchCommand
	sequence                           int64
}

func newTypedContinuationFixture(t *testing.T, provider adapter.Provider) *typedContinuationFixture {
	return newTypedContinuationFixtureTask(t, provider, "task")
}

func newTypedContinuationFixtureTask(t *testing.T, provider adapter.Provider, taskID string) *typedContinuationFixture {
	return newTypedContinuationFixtureWithConfig(t, provider, taskID, nil)
}

func newTypedContinuationFixtureWithConfig(t *testing.T, provider adapter.Provider, taskID string, ref *contract.ConfigGenerationRef) *typedContinuationFixture {
	t.Helper()
	f := &typedContinuationFixture{path: filepath.Join(t.TempDir(), "state.db"), sessionKind: "session-id", sessionID: "native-session"}
	if provider == adapter.ProviderAGY {
		f.sessionKind = "conversation-id"
	}
	pin := adapter.BinaryPin{Path: "/private/bin/provider", Version: "bounded-fixture-v1", SHA256: strings.Repeat("a", 64)}
	if provider == adapter.ProviderClaude {
		pin.Version = "2.1.285 (Claude Code)"
	}
	payload := adapter.InvocationPayload{Provider: string(provider), Directory: "/private/workspace", Prompt: "Original instruction", Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000}, ProviderLock: &adapter.ProviderLock{Version: 1, Provider: provider, Protocol: adapter.ProtocolID(provider), Binary: pin}}
	if provider == adapter.ProviderCodex {
		payload.Kind = "provider"
		payload.Profile.CLIModelDefault = true
		payload.ProviderLock.Binary.Version, payload.ProviderLock.Binary.SHA256 = adapter.CodexVersion, adapter.CodexSHA256
		f.sessionID = "12345678-1234-4321-8123-123456789abc"
	}
	f.payload, _ = json.Marshal(payload)
	if ref != nil {
		p, err := adapter.DecodeInvocationPayload(f.payload)
		if err != nil {
			t.Fatal(err)
		}
		p.ConfigGeneration, p.ConfigModelSource, p.Profile.Model = ref, "saved-default", "a"
		if provider == adapter.ProviderCodex {
			// A pinned generation does not override this private backend's CLI default.
			p.ConfigModelSource, p.Profile.Model = "cli-default", ""
		}
		f.payload, _ = json.Marshal(p)
	}
	var err error
	f.db, err = Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	f.receipt, err = submitExistingConfigHistory(t, f.db, context.Background(), PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/bin/orchestrator"}, Tasks: []TaskSpec{{ID: taskID, RunID: "run", MaxAttempts: 3, MaxActiveMS: 1000, AdapterPayload: f.payload}}})
	if err != nil {
		t.Fatal(err)
	}
	f.host, err = f.db.RegisterHost(context.Background(), contract.HostHello{LaunchID: f.receipt.LaunchID, LaunchToken: f.receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "generation", PID: 10, Birth: "host", Executable: "/private/bin/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	f.grant, err = f.db.ClaimReady(context.Background(), f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *typedContinuationFixture) event(kind string) contract.Event {
	f.sequence++
	return contract.Event{Version: 1, ProducerID: f.host, EventID: fmt.Sprintf("%s:%d", f.host, f.sequence), RunID: f.grant.RunID, TaskID: f.grant.TaskID, AttemptID: f.grant.AttemptID, SegmentID: f.grant.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: f.grant.CommandID, Sequence: f.sequence, Kind: kind, PayloadHash: fmt.Sprintf("%064x", f.sequence)}
}

func (f *typedContinuationFixture) send(t *testing.T, event contract.Event) {
	t.Helper()
	if _, err := f.db.CommitHostEvent(context.Background(), event); err != nil {
		t.Fatalf("commit %s: %v", event.Kind, err)
	}
}

func (f *typedContinuationFixture) start(t *testing.T) contract.Event {
	t.Helper()
	for _, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning} {
		f.send(t, f.event(kind))
	}
	init := f.event(contract.EventSession)
	init.SessionKind, init.SessionID = f.sessionKind, f.sessionID
	f.send(t, init)
	return init
}

func (f *typedContinuationFixture) question(t *testing.T, revision int) contract.Event {
	t.Helper()
	q := f.event(contract.EventQuestion)
	q.QuestionID, q.QuestionRevision, q.QuestionKind = fmt.Sprintf("question-%d", revision), revision, "technical"
	q.SessionKind, q.SessionID = f.sessionKind, f.sessionID
	f.send(t, q)
	exited := f.event(contract.EventExited)
	exit := 0
	exited.ExitCode, exited.ActiveMS = &exit, 200
	f.send(t, exited)
	return q
}

func TestTypedQuestionContinuationCarriesTrustedGrantAcrossTwoCycles(t *testing.T) {
	for _, provider := range []adapter.Provider{adapter.ProviderAGY, adapter.ProviderClaude, adapter.ProviderCodex} {
		t.Run(string(provider), func(t *testing.T) {
			f := newTypedContinuationFixture(t, provider)
			first := f.grant
			init := f.start(t)
			for revision := 1; revision <= 2; revision++ {
				q := f.question(t, revision)
				answer := fmt.Sprintf("Answer %d", revision)
				spec := AnswerSpec{TaskID: first.TaskID, WorkRevision: 1, QuestionID: q.QuestionID, QuestionRevision: revision, Answer: answer}
				if _, err := f.db.AnswerQuestion(context.Background(), spec); err != nil {
					t.Fatal(err)
				}
				g, err := f.db.ClaimReady(context.Background(), f.host, 1, 2)
				if err != nil {
					t.Fatal(err)
				}
				if g.AttemptID != first.AttemptID || g.SegmentID == f.grant.SegmentID || g.SessionKind != f.sessionKind || g.SessionID != f.sessionID || g.Answer != answer || g.GrantedActiveMS != 1000-int64(revision)*200 {
					t.Fatalf("lost attempt/session/answer/budget: %+v", g)
				}
				raw, _ := json.Marshal(g)
				var wire struct {
					Continuation *struct {
						Version              int    `json:"version"`
						Mode                 string `json:"mode"`
						SourceSegmentID      string `json:"source_segment_id"`
						SourceEventID        string `json:"source_event_id"`
						SessionEventID       string `json:"session_event_id"`
						AdapterPayloadSHA256 string `json:"adapter_payload_sha256"`
						OwnerContextID       string `json:"owner_context_id"`
					} `json:"continuation"`
				}
				if err = json.Unmarshal(raw, &wire); err != nil {
					t.Fatal(err)
				}
				hash := sha256.Sum256(f.payload)
				if wire.Continuation == nil {
					t.Fatal("typed answer queued a segment without a trusted continuation reference")
				}
				c := wire.Continuation
				if c.Version != 1 || c.Mode != "question" || c.SourceSegmentID != q.SegmentID || c.SourceEventID != q.EventID || c.SessionEventID != init.EventID || c.AdapterPayloadSHA256 != hex.EncodeToString(hash[:]) || c.OwnerContextID != "origin" {
					t.Fatalf("unbound continuation: %+v", c)
				}
				pending, err := f.db.PendingLaunches(context.Background(), f.host, 1)
				if err != nil || len(pending) != 1 {
					t.Fatalf("pending=%+v err=%v", pending, err)
				}
				replay, _ := json.Marshal(pending[0])
				if string(replay) != string(raw) {
					t.Fatalf("replay changed grant: %s != %s", replay, raw)
				}
				f.grant = g
				f.start(t)
			}
		})
	}
}

func TestTypedSessionBindingRejectsProviderKindAndCrossSegmentDrift(t *testing.T) {
	t.Run("provider_kind", func(t *testing.T) {
		f := newTypedContinuationFixture(t, adapter.ProviderAGY)
		for _, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning} {
			f.send(t, f.event(kind))
		}
		e := f.event(contract.EventSession)
		e.SessionKind, e.SessionID = "session-id", f.sessionID
		if _, err := f.db.CommitHostEvent(context.Background(), e); err == nil {
			t.Fatal("AGY accepted an untrusted session reference kind")
		}
	})
	t.Run("cross_segment", func(t *testing.T) {
		f := newTypedContinuationFixture(t, adapter.ProviderClaude)
		f.start(t)
		q := f.question(t, 1)
		if _, err := f.db.AnswerQuestion(context.Background(), AnswerSpec{TaskID: "task", WorkRevision: 1, QuestionID: q.QuestionID, QuestionRevision: 1, Answer: "Continue"}); err != nil {
			t.Fatal(err)
		}
		var err error
		f.grant, err = f.db.ClaimReady(context.Background(), f.host, 1, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning} {
			f.send(t, f.event(kind))
		}
		e := f.event(contract.EventSession)
		e.SessionKind, e.SessionID = f.sessionKind, "different-session"
		if _, err = f.db.CommitHostEvent(context.Background(), e); err == nil {
			t.Fatal("resumed attempt replaced its trusted native session")
		}
	})
}

func TestTypedQuestionCannotAuthorizeItsOwnSession(t *testing.T) {
	f := newTypedContinuationFixture(t, adapter.ProviderClaude)
	for _, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning} {
		f.send(t, f.event(kind))
	}
	q := f.event(contract.EventQuestion)
	q.QuestionID, q.QuestionRevision, q.QuestionKind = "forged-question", 1, "technical"
	q.SessionKind, q.SessionID = f.sessionKind, f.sessionID
	if _, err := f.db.CommitHostEvent(context.Background(), q); err == nil {
		t.Fatal("question created resumable state without trusted initialization")
	}
}
