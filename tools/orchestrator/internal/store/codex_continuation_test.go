package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Pure SQLite transactions and synthetic authenticated Host events, no process
// creation or kernel exit claim. Host separately owns physical seals/Wait.
func TestCodexStoreInitialPayloadAdmissionKeepsGeneralGuardClosed(t *testing.T) {
	f := newTypedContinuationFixture(t, adapter.ProviderCodex)
	original, err := adapter.DecodeInvocationPayload(f.payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := continuationPayload(f.payload); err != nil {
		t.Fatal("exact private initial payload", err)
	}
	if _, err := adapter.BuildInvocation(adapter.Request{Provider: adapter.ProviderCodex, Binary: original.ProviderLock.Binary, Lock: original.ProviderLock, Profile: original.Profile, CWD: original.Directory, Prompt: original.Prompt}); err == nil {
		t.Fatal("public Codex guard opened")
	}
	for _, kind := range []string{"kind", "default", "model", "reasoning", "role", "permission", "lock_version", "lock_provider", "lock_protocol", "binary_version", "binary_hash", "binary_path", "extra", "args", "session", "allow", "deny", "permission_mode", "candidate", "workspace", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			p, _ := adapter.DecodeInvocationPayload(f.payload)
			switch kind {
			case "kind":
				p.Kind = ""
			case "default":
				p.Profile.CLIModelDefault = false
			case "model":
				p.Profile.Model = "override"
			case "reasoning":
				p.Profile.Reasoning = "override"
			case "role":
				p.Profile.Role = adapter.Implementer
			case "permission":
				p.Profile.Permission = adapter.WorkspaceWrite
			case "lock_version":
				p.ProviderLock.Version++
			case "lock_provider":
				p.ProviderLock.Provider = adapter.ProviderAGY
			case "lock_protocol":
				p.ProviderLock.Protocol = "foreign"
			case "binary_version":
				p.ProviderLock.Binary.Version = "foreign"
			case "binary_hash":
				p.ProviderLock.Binary.SHA256 = runtimeHash("foreign")
			case "binary_path":
				p.BinaryPath = "/private/other"
			case "extra":
				p.ExtraArgs = []string{"--dangerously-bypass-approvals-and-sandbox"}
			case "args":
				p.Args = []string{"exec"}
			case "session":
				p.SessionKind, p.SessionID = "session-id", f.sessionID
			case "allow":
				p.Allow = []string{"Bash(*)"}
			case "deny":
				p.Deny = []string{"Read(*)"}
			case "permission_mode":
				p.PermissionMode = "bypassPermissions"
			case "candidate":
				p.CandidateAction = &adapter.CandidateAction{Version: 1, Operation: "review", SourceTask: "edit"}
			case "workspace":
				p.CandidateWorkspace = &adapter.CandidateWorkspace{}
			case "timeout":
				p.Profile.TimeoutMS = 0
			}
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := continuationPayload(raw); err == nil {
				t.Fatal("out-of-scope descriptor admitted")
			}
		})
	}
}

func TestCodexStoreQuestionDecisionPersistsAndDuplicateIsExact(t *testing.T) {
	f := newTypedContinuationFixture(t, adapter.ProviderCodex)
	first := f.grant
	initial := f.start(t)
	question := f.question(t, 1)
	spec := AnswerSpec{TaskID: first.TaskID, WorkRevision: 1, QuestionID: question.QuestionID, QuestionRevision: 1, Answer: "Authorized answer"}
	a, err := f.db.AnswerQuestion(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.db.AnswerQuestion(context.Background(), spec)
	if err != nil || a != b {
		t.Fatal("duplicate answer not idempotent", err)
	}
	var count int
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM continuation_decisions`).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate decision")
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db, err = Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	g, err := f.db.ClaimReady(context.Background(), f.host, 1, 2)
	if err != nil || g.AttemptID != first.AttemptID || g.GrantedActiveMS != 800 || g.SessionID != f.sessionID || g.Continuation == nil || g.Continuation.SourceKind != "host" || g.Continuation.SessionEventID != initial.EventID || g.Continuation.SourceEventID != question.EventID {
		t.Fatal("persisted decision/grant lost provenance/budget", err)
	}
	pending, err := f.db.PendingLaunches(context.Background(), f.host, 1)
	if err != nil || len(pending) != 1 || !reflect.DeepEqual(g, pending[0]) {
		t.Fatal("claim replay drift", err)
	}
	if _, err := f.db.ClaimReady(context.Background(), f.host, 1, 2); err == nil {
		t.Fatal("duplicate claim created new reservation")
	}
	spec.Answer += " conflict"
	if _, err := f.db.AnswerQuestion(context.Background(), spec); err == nil {
		t.Fatal("conflicting answer changed commitment")
	}
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM attempts`).Scan(&count); err != nil || count != 1 {
		t.Fatal("continuation spent a new attempt")
	}
	var used, granted int64
	if err := f.db.sql.QueryRow(`SELECT used_ms FROM budget_runtime WHERE id=?`, first.ReservationID).Scan(&used); err != nil || used != 200 {
		t.Fatal("old charge rewritten")
	}
	if err := f.db.sql.QueryRow(`SELECT granted_ms FROM budget_runtime WHERE id=?`, g.ReservationID).Scan(&granted); err != nil || granted != 800 {
		t.Fatal("remaining budget reset")
	}
}

func TestCodexStoreQuestionRefusesUnsafeSourceAndRollsBack(t *testing.T) {
	for _, kind := range []string{"unsettled", "unknown", "budget", "owner", "payload", "session_binding"} {
		t.Run(kind, func(t *testing.T) {
			f := newTypedContinuationFixture(t, adapter.ProviderCodex)
			f.start(t)
			q := f.event(contract.EventQuestion)
			q.QuestionID, q.QuestionRevision, q.QuestionKind, q.SessionKind, q.SessionID = "question", 1, "technical", f.sessionKind, f.sessionID
			f.send(t, q)
			if kind == "unknown" {
				f.send(t, f.event(contract.EventUnknown))
			} else if kind != "unsettled" {
				x := f.event(contract.EventExited)
				zero := 0
				x.ExitCode = &zero
				x.ActiveMS = 200
				if kind == "budget" {
					x.ActiveMS = 1000
				}
				f.send(t, x)
			}
			switch kind {
			case "owner":
				if _, err := f.db.sql.Exec(`UPDATE runs SET origin_context_id='foreign' WHERE id=?`, f.grant.RunID); err != nil {
					t.Fatal(err)
				}
			case "payload":
				p, _ := adapter.DecodeInvocationPayload(f.payload)
				p.Prompt = "drift"
				raw, _ := json.Marshal(p)
				if _, err := f.db.sql.Exec(`UPDATE task_runtime SET adapter_payload=? WHERE task_id=?`, string(raw), f.grant.TaskID); err != nil {
					t.Fatal(err)
				}
			case "session_binding":
				if _, err := f.db.sql.Exec(`UPDATE attempt_provider_bindings SET session_id='22345678-1234-4321-8123-123456789abc' WHERE attempt_id=?`, f.grant.AttemptID); err != nil {
					t.Fatal(err)
				}
			}
			before := continuationQuotaState(t, f.db)
			if _, err := f.db.AnswerQuestion(context.Background(), AnswerSpec{TaskID: f.grant.TaskID, WorkRevision: 1, QuestionID: q.QuestionID, QuestionRevision: 1, Answer: "Answer"}); err == nil {
				t.Fatal("unsafe/unsettled/exhausted source queued")
			}
			if after := continuationQuotaState(t, f.db); after != before {
				t.Fatal("failed admission escaped transaction")
			}
		})
	}
}

func TestCodexStoreClaimRefusesPersistedRefDriftAtomically(t *testing.T) {
	for _, kind := range []string{"owner", "source_event", "source_segment", "source_hash", "session_event", "payload_hash", "mode", "kind", "revision"} {
		t.Run(kind, func(t *testing.T) {
			f := newTypedContinuationFixture(t, adapter.ProviderCodex)
			f.start(t)
			q := f.question(t, 1)
			if _, err := f.db.AnswerQuestion(context.Background(), AnswerSpec{TaskID: f.grant.TaskID, WorkRevision: 1, QuestionID: q.QuestionID, QuestionRevision: 1, Answer: "Answer"}); err != nil {
				t.Fatal(err)
			}
			var raw string
			if err := f.db.sql.QueryRow(`SELECT ref_json FROM continuation_decisions`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var ref contract.ContinuationRef
			if json.Unmarshal([]byte(raw), &ref) != nil {
				t.Fatal("fixture ref")
			}
			switch kind {
			case "owner":
				ref.OwnerContextID = "foreign"
			case "source_event":
				ref.SourceEventID = "foreign"
			case "source_segment":
				ref.SourceSegmentID = "foreign"
			case "source_hash":
				ref.SourceEventSHA256 = runtimeHash("foreign")
			case "session_event":
				ref.SessionEventID = "foreign"
			case "payload_hash":
				ref.AdapterPayloadSHA256 = runtimeHash("foreign")
			case "mode":
				ref.Mode = contract.ContinuationInterrupted
			case "kind":
				ref.SourceKind = "report"
			case "revision":
				ref.SourceEventRevision++
			}
			body, _ := json.Marshal(ref)
			if _, err := f.db.sql.Exec(`UPDATE continuation_decisions SET ref_json=?`, string(body)); err != nil {
				t.Fatal(err)
			}
			before := continuationQuotaState(t, f.db)
			if _, err := f.db.ClaimReady(context.Background(), f.host, 1, 2); err == nil {
				t.Fatal("foreign/drift ref claimed")
			}
			if after := continuationQuotaState(t, f.db); after != before {
				t.Fatal("claim leaked reservation/attempt/ready mutation")
			}
		})
	}
}

func TestCodexStoreUnsupportedContinuationSourcesDoNotQueue(t *testing.T) {
	t.Run("interrupted", func(t *testing.T) {
		f := newTypedContinuationFixture(t, adapter.ProviderCodex)
		f.start(t)
		s := f.interrupted(t, 200)
		before := continuationQuotaState(t, f.db)
		if _, err := f.db.QueueContinuation(context.Background(), s); err == nil {
			t.Fatal("interrupted Codex admitted without seal contract")
		}
		if after := continuationQuotaState(t, f.db); after != before {
			t.Fatal("rejected interrupted source changed state")
		}
	})
	t.Run("report", func(t *testing.T) {
		f := newTypedContinuationFixture(t, adapter.ProviderCodex)
		f.start(t)
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil || os.Chmod(dir, 0700) != nil {
			t.Fatal("fixture cap dir")
		}
		g := f.grant
		if _, err := f.db.RegisterReportCapability(context.Background(), contract.ReportCapabilityRegistration{CapabilityID: g.ReportCapabilityID, ProducerID: f.host, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, TokenHash: runtimeHash("token"), CapabilityDir: dir}); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "question.txt")
		text := []byte("Synthetic helper question")
		if os.WriteFile(path, text, 0600) != nil {
			t.Fatal("owned artifact")
		}
		payload, _ := json.Marshal(map[string]any{"status": "waiting_user", "question_id": "report-question", "question_revision": 1, "question_kind": "technical", "artifact": contract.ArtifactRef{ID: "question", Path: path, Size: int64(len(text)), SHA256: runtimeHash(string(text))}})
		if _, err := f.db.CommitReportEvent(context.Background(), ReportEventSpec{CapabilityID: g.ReportCapabilityID, Token: "token", EventID: "report-question-event", Sequence: 1, Kind: "question", Payload: payload}); err != nil {
			t.Fatal("actual report fixture", err)
		}
		stop := f.event(contract.EventStopped)
		stop.StopReason = "owner_stop_requested"
		f.send(t, stop)
		x := f.event(contract.EventExited)
		code := 130
		x.ExitCode, x.ActiveMS = &code, 200
		f.send(t, x)
		before := continuationQuotaState(t, f.db)
		if _, err := f.db.AnswerQuestion(context.Background(), AnswerSpec{TaskID: g.TaskID, WorkRevision: 1, QuestionID: "report-question", QuestionRevision: 1, Answer: "Answer"}); err == nil {
			t.Fatal("helper report substituted native structured Question")
		}
		if after := continuationQuotaState(t, f.db); after != before {
			t.Fatal("rejected report source changed state")
		}
	})
}

func TestCodexStoreUnboundOrInvalidNativeSessionCannotCreateQuestion(t *testing.T) {
	for _, kind := range []string{"missing", "kind", "uuid", "question_drift"} {
		t.Run(kind, func(t *testing.T) {
			f := newTypedContinuationFixture(t, adapter.ProviderCodex)
			for _, phase := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning} {
				f.send(t, f.event(phase))
			}
			if kind != "missing" {
				session := f.event(contract.EventSession)
				session.SessionKind, session.SessionID = f.sessionKind, f.sessionID
				if kind == "kind" {
					session.SessionKind = "conversation-id"
				}
				if kind == "uuid" {
					session.SessionID = "native-session"
				}
				before := continuationQuotaState(t, f.db)
				_, err := f.db.CommitHostEvent(context.Background(), session)
				if kind != "question_drift" {
					if err == nil || continuationQuotaState(t, f.db) != before {
						t.Fatal("invalid native Session changed binding/state")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			question := f.event(contract.EventQuestion)
			question.QuestionID, question.QuestionRevision, question.QuestionKind, question.SessionKind, question.SessionID = "question", 1, "technical", f.sessionKind, f.sessionID
			if kind == "question_drift" {
				question.SessionID = "22345678-1234-4321-8123-123456789abc"
			}
			before := continuationQuotaState(t, f.db)
			if _, err := f.db.CommitHostEvent(context.Background(), question); err == nil || continuationQuotaState(t, f.db) != before {
				t.Fatal("Question authorized missing/foreign Session")
			}
		})
	}
}
