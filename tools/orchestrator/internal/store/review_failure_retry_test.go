package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

const reviewDiagnosticBody = `{"code":"candidate_review_invalid","reason":"candidate_freeze_failed","status":"failed"}`

func diagnosticReviewFixture(t *testing.T, provider adapter.Provider, decorate ...func([]TaskSpec) []TaskSpec) reviewRetryFixture {
	t.Helper()
	var plan func([]TaskSpec) []TaskSpec
	if len(decorate) != 0 {
		plan = decorate[0]
	}

	return diagnosticReviewFixturePrepared(t, provider, plan, nil)
}

func diagnosticReviewFixturePrepared(t *testing.T, provider adapter.Provider, plan func([]TaskSpec) []TaskSpec, bind func(*DB, PlanSpec) PlanSpec) reviewRetryFixture {
	t.Helper()
	return diagnosticReviewFixtureBody(t, provider, plan, bind, reviewDiagnosticBody, 7)
}

func diagnosticReviewFixtureBody(t *testing.T, provider adapter.Provider, plan func([]TaskSpec) []TaskSpec, bind func(*DB, PlanSpec) PlanSpec, body string, exitCode int, session ...bool) reviewRetryFixture {
	t.Helper()
	f := newReviewRetryFixtureBound(t, provider, "", nil, true, true, plan, bind)
	g := f.grant
	// testing.TempDir subdirectories do not establish the runtime 0700 contract.
	if err := os.Chmod(filepath.Dir(f.db.path), 0700); err != nil {
		t.Fatal(err)
	}
	ref := managedHostArtifactForTest(t, f.db, g, "terminal", 0, []byte(body))
	e := contract.Event{Version: 1, ProducerID: f.host, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, ExecutionEpoch: 1, CommandID: g.CommandID, Process: &contract.ProcessIdentity{PID: 300, PGID: 300, Birth: "review-worker"}, Sequence: 4}
	kinds := []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventFailed, contract.EventExited}
	if len(session) > 0 && session[0] {
		kinds = []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventSession, contract.EventFailed, contract.EventExited}
	}
	for _, kind := range kinds {
		e.Sequence++
		e.Kind, e.EventID, e.PayloadHash = kind, "diagnostic-"+kind, runtimeHash(kind)
		if body != reviewDiagnosticBody {
			if kind == contract.EventPrepared {
				e.Process = nil
			} else {
				e.Process = &contract.ProcessIdentity{PID: 300, PGID: 300, Birth: "review-worker"}
			}
		}
		if kind == contract.EventSession {
			e.SessionKind = "session-id"
			e.SessionID = "review-session"
		}
		if kind == contract.EventFailed || kind == contract.EventExited {
			exit := exitCode
			e.ExitCode, e.ActiveMS = &exit, 1
			e.Artifact = ref
			e.PayloadHash = e.Artifact.SHA256
			if kind == contract.EventExited && body != reviewDiagnosticBody {
				e.PayloadHash = runtimeHash("exited:" + g.SegmentID)
			}
		}
		if _, err := f.db.CommitHostEvent(context.Background(), e); err != nil {
			t.Fatal(kind, err)
		}
	}
	page, err := f.db.CollectPending(context.Background(), g.TaskID, "", 0, false)
	if err != nil || len(page.Events) != 1 || page.Events[0].Kind != contract.EventFailed {
		t.Fatal("missing original failure", page, err)
	}
	f.event = page.Events[0]
	f.spec = RetrySpec{TaskID: g.TaskID, WorkRevision: g.WorkRevision, EventID: f.event.EventID, EventRevision: f.event.EventRevision, EventHash: f.event.PayloadHash, ActionSlot: f.event.ActionSlot, SegmentID: g.SegmentID, NextAttemptNo: 2, CommandID: "diagnostic-retry"}
	var automatic int
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM retry_decisions`).Scan(&automatic); err != nil || automatic != 0 {
		t.Fatal("post-start failure acquired automatic retry", automatic, err)
	}
	return f
}

func TestCandidateReviewDiagnosticFailureExplicitRetry(t *testing.T) {
	for _, provider := range []adapter.Provider{adapter.ProviderClaude, adapter.ProviderGrok} {
		t.Run(string(provider), func(t *testing.T) {
			f := diagnosticReviewFixture(t, provider)
			ctx := context.Background()
			receipt, err := f.db.QueueRetry(ctx, f.spec)
			if err != nil {
				t.Fatal("Host diagnostic blocked explicit failed-review retry", err)
			}
			path := f.db.path
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			f.db, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.db.Close()
			f.spec.CommandID = "diagnostic-replay"
			if replay, err := f.db.QueueRetry(ctx, f.spec); err != nil || replay != receipt {
				t.Fatal("reopen lost exact retry receipt", replay, err)
			}
			next, err := f.db.ClaimReady(ctx, f.host, 1, 2)
			if err != nil || next.AttemptID == f.grant.AttemptID || !reflect.DeepEqual(next.Input, f.grant.Input) || !reflect.DeepEqual(next.DependencyInputs, f.grant.DependencyInputs) {
				t.Fatal("diagnostic retry lost frozen input or new attempt", err)
			}
			oldPayload, _ := adapter.DecodeInvocationPayload(f.grant.AdapterPayload)
			newPayload, _ := adapter.DecodeInvocationPayload(next.AdapterPayload)
			if newPayload.Directory == oldPayload.Directory {
				t.Fatal("diagnostic retry reused old review directory")
			}
			newPayload.Directory = oldPayload.Directory
			if !reflect.DeepEqual(oldPayload, newPayload) {
				t.Fatal("diagnostic retry changed frozen adapter")
			}
		})
	}
}

// These cases damage one piece of persisted evidence after a genuine Store
// lifecycle. Retry must reject atomically rather than trusting the old status.
func TestCandidateReviewDiagnosticRetryRejectsUnsafeEvidence(t *testing.T) {
	cases := []string{"content", "hash", "size", "oversize", "path", "id", "symlink", "parent_symlink", "mode", "hardlink", "unknown_field", "duplicate_field", "case_field", "other_code", "successful_result", "exit_zero", "exit_negative", "no_process", "no_exited", "exit_process", "exit_artifact", "exit_code", "exit_sequence", "result", "unknown", "stopped", "host_offline"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			f := diagnosticReviewFixture(t, adapter.ProviderGrok)
			e := f.event
			var done contract.Event
			var raw string
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(f.db.sql.QueryRow(`SELECT body_json FROM runtime_events WHERE event_id='diagnostic-exited'`).Scan(&raw))
			must(json.Unmarshal([]byte(raw), &done))
			body := reviewDiagnosticBody
			path := e.Artifact.Path
			switch name {
			case "content":
				must(os.WriteFile(path, []byte(strings.Repeat("x", len(body))), 0600))
			case "hash":
				e.Artifact.SHA256 = strings.Repeat("a", 64)
			case "size":
				e.Artifact.Size++
			case "oversize":
				e.Artifact.Size = 257
			case "path":
				e.Artifact.Path = filepath.Join(t.TempDir(), "result-output.bin")
				must(os.WriteFile(e.Artifact.Path, []byte(body), 0600))
			case "id":
				e.Artifact.ID = "other"
			case "symlink":
				must(os.Rename(path, path+".old"))
				must(os.Symlink(path+".old", path))
			case "parent_symlink":
				dir := filepath.Dir(path)
				must(os.Rename(dir, dir+".old"))
				must(os.Symlink(dir+".old", dir))
			case "mode":
				must(os.Chmod(path, 0644))
			case "hardlink":
				must(os.Link(path, path+".link"))
			case "unknown_field":
				body = strings.TrimSuffix(body, "}") + `,"extra":true}`
			case "duplicate_field":
				body = strings.TrimSuffix(body, "}") + `,"status":"failed"}`
			case "case_field":
				body = strings.Replace(body, `"status"`, `"Status"`, 1)
			case "other_code":
				body = strings.Replace(body, "candidate_review_invalid", "candidate_input_order_changed", 1)
			case "successful_result":
				body = `{"status":"completed","stage":"review","review":{"decision":"approve"}}`
			case "exit_zero":
				zero := 0
				e.ExitCode = &zero
				done.ExitCode = &zero
			case "exit_negative":
				negative := -1
				e.ExitCode = &negative
				done.ExitCode = &negative
			case "no_process":
				e.Process = nil
				done.Process = nil
			case "no_exited":
				_, err := f.db.sql.Exec(`DELETE FROM runtime_events WHERE event_id='diagnostic-exited'`)
				must(err)
			case "exit_process":
				done.Process.Birth = "different"
			case "exit_artifact":
				done.Artifact.SHA256 = strings.Repeat("b", 64)
			case "exit_code":
				other := 8
				done.ExitCode = &other
			case "exit_sequence":
				done.Sequence = e.Sequence - 1
			case "result", "unknown", "stopped":
				late := done
				late.Kind, late.EventID, late.Sequence = name, "late-"+name, done.Sequence+1
				_, err := f.db.CommitHostEvent(context.Background(), late)
				must(err)
			case "host_offline":
				_, err := f.db.sql.Exec(`UPDATE runtime_hosts SET status='offline' WHERE id=?`, f.host)
				must(err)
			}
			if body != reviewDiagnosticBody {
				must(os.WriteFile(path, []byte(body), 0600))
				e.Artifact.Size, e.Artifact.SHA256, e.PayloadHash = int64(len(body)), runtimeHash(body), runtimeHash(body)
				done.Artifact = e.Artifact
			}
			// Preserve actual source tuple admission so rejection must inspect the
			// damaged evidence, not merely encounter a stale caller hash.
			f.spec.EventHash = e.PayloadHash
			for _, event := range []contract.Event{e, done} {
				raw, err := json.Marshal(event)
				must(err)
				_, err = f.db.sql.Exec(`UPDATE runtime_events SET body_json=?,payload_hash=? WHERE event_id=?`, string(raw), event.PayloadHash, event.EventID)
				must(err)
			}
			if _, err := f.db.QueueRetry(context.Background(), f.spec); err == nil {
				t.Fatal("unsafe failure evidence authorized retry")
			}
			var decisions, attempts int
			must(f.db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM retry_decisions),(SELECT COUNT(*) FROM attempts WHERE task_id='review')`).Scan(&decisions, &attempts))
			if decisions != 0 || attempts != 1 {
				t.Fatal("failed authorization mutated retry budget", decisions, attempts)
			}
		})
	}
}

func TestCandidateReviewDiagnosticRetryRechecksEvidenceBeforeClaim(t *testing.T) {
	for _, name := range []string{"artifact", "host"} {
		t.Run(name, func(t *testing.T) {
			f := diagnosticReviewFixture(t, adapter.ProviderGrok)
			if _, err := f.db.QueueRetry(context.Background(), f.spec); err != nil {
				t.Fatal(err)
			}
			if name == "artifact" {
				if err := os.WriteFile(f.event.Artifact.Path, []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.db.sql.Exec(`UPDATE runtime_hosts SET birth='rebound' WHERE id=?`, f.host); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.db.ClaimReady(context.Background(), f.host, 1, 2); err == nil {
				t.Fatal("changed evidence consumed retry promise")
			}
			var count int
			if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM attempts WHERE task_id='review'`).Scan(&count); err != nil || count != 1 {
				t.Fatal("rejected claim consumed an attempt", count, err)
			}
		})
	}
}

const reviewDiagnosticV2Body = `{"code":"candidate_review_invalid","reason":"candidate_freeze_failed","status":"failed","writer_version":2}`

func TestCandidateReviewDiagnosticV2ExplicitRetry(t *testing.T) {
	for _, provider := range []adapter.Provider{adapter.ProviderClaude, adapter.ProviderGrok} {
		for _, exit := range []int{0, 7} {
			for _, session := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/exit=%d/session=%t", provider, exit, session), func(t *testing.T) {
					f := diagnosticReviewFixtureBody(t, provider, nil, nil, reviewDiagnosticV2Body, exit, session)
					if _, err := f.db.QueueRetry(context.Background(), f.spec); err != nil {
						t.Fatal("complete v2 lifecycle rejected", err)
					}
					if _, err := f.db.ClaimReady(context.Background(), f.host, 1, 2); err != nil {
						t.Fatal("v2 retry failed claim revalidation", err)
					}
				})
			}
		}
	}
}

func TestCandidateReviewDiagnosticV2RejectsInvalidLifecycle(t *testing.T) {
	cases := []string{"missing_prepared", "missing_spawned", "missing_running", "missing_failed", "missing_exited", "duplicate_prepared", "duplicate_spawned", "duplicate_running", "duplicate_failed", "duplicate_exited", "prepared_process", "spawned_process", "running_process", "exit_process", "order_prepared", "order_spawned", "order_running", "sequence_collision", "run", "task", "attempt", "segment", "command", "work_revision", "producer", "exit_hash", "failed_hash", "exit_artifact", "exit_code", "negative_exit", "session_before_running", "session_after_failed", "question", "result", "stopped", "unknown"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			f := diagnosticReviewFixtureBody(t, adapter.ProviderGrok, nil, nil, reviewDiagnosticV2Body, 0)
			ctx := context.Background()
			rows, err := f.db.sql.Query(`SELECT body_json FROM runtime_events WHERE segment_id=? ORDER BY sequence`, f.grant.SegmentID)
			if err != nil {
				t.Fatal(err)
			}
			var events []contract.Event
			for rows.Next() {
				var raw string
				var e contract.Event
				if err := rows.Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(raw), &e); err != nil {
					t.Fatal(err)
				}
				events = append(events, e)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			index := map[string]int{"prepared": 0, "spawned": 1, "running": 2, "failed": 3, "exited": 4}
			if strings.HasPrefix(name, "missing_") {
				_, err = f.db.sql.Exec(`DELETE FROM runtime_events WHERE event_id=?`, events[index[strings.TrimPrefix(name, "missing_")]].EventID)
			} else {
				i := 1
				switch name {
				case "prepared_process":
					events[0].Process = events[1].Process
				case "spawned_process":
					events[1].Process.Birth = "other"
				case "running_process":
					events[2].Process = nil
				case "exit_process":
					events[4].Process.PID++
				case "order_prepared":
					events[0].Sequence = events[2].Sequence
				case "order_spawned":
					events[1].Sequence = events[3].Sequence
				case "order_running":
					events[2].Sequence = events[3].Sequence
				case "sequence_collision":
					events[1].Sequence = events[0].Sequence
				case "run":
					events[i].RunID = "other"
				case "task":
					events[i].TaskID = "other"
				case "attempt":
					events[i].AttemptID = "other"
				case "segment":
					events[i].SegmentID = "other"
				case "command":
					events[i].CommandID = "other"
				case "work_revision":
					events[i].WorkRevision++
				case "producer":
					events[i].ProducerID = "other"
				case "exit_hash":
					events[4].PayloadHash = events[3].PayloadHash
				case "failed_hash":
					events[3].PayloadHash = runtimeHash("other")
				case "exit_artifact":
					events[4].Artifact.ID = "other"
				case "exit_code":
					value := 7
					events[4].ExitCode = &value
				case "negative_exit":
					value := -1
					events[3].ExitCode = &value
					events[4].ExitCode = &value
				default:
					var extra contract.Event
					if strings.HasPrefix(name, "duplicate_") {
						extra = events[index[strings.TrimPrefix(name, "duplicate_")]]
						extra.Sequence = events[4].Sequence + 10
					} else {
						extra = events[2]
						extra.Kind = contract.EventSession
						extra.SessionKind = "session-id"
						extra.SessionID = "session"
						if name == "session_before_running" {
							extra.Sequence = events[1].Sequence + 1
						} else if strings.HasPrefix(name, "session_") && name != "session_after_failed" {
							identity := *extra.Process
							identity.Birth = "other"
							extra.Process = &identity
							extra.Sequence = events[2].Sequence + 1
						} else {
							extra.Sequence = events[3].Sequence + 1
						}
						if name == "question" || name == "result" || name == "stopped" || name == "unknown" {
							extra.Kind = name
						}
					}
					extra.EventID = "extra-" + name
					raw, _ := json.Marshal(extra)
					_, err = f.db.sql.Exec(`INSERT INTO runtime_events(event_id,producer_id,sequence,task_id,attempt_id,segment_id,payload_hash,body_json,delivery_status,created_at) SELECT ?,producer_id,?,task_id,attempt_id,segment_id,payload_hash,?,delivery_status,created_at FROM runtime_events WHERE event_id=?`, extra.EventID, events[4].Sequence+100, string(raw), events[2].EventID)
				}
				for _, e := range events {
					raw, _ := json.Marshal(e)
					if _, updateErr := f.db.sql.Exec(`UPDATE runtime_events SET body_json=? WHERE event_id=?`, string(raw), e.EventID); updateErr != nil {
						t.Fatal(updateErr)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.QueueRetry(ctx, f.spec); err == nil {
				t.Fatal("invalid v2 lifecycle authorized retry")
			}
			var decisions, attempts int
			if err := f.db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM retry_decisions),(SELECT COUNT(*) FROM attempts WHERE task_id='review')`).Scan(&decisions, &attempts); err != nil || decisions != 0 || attempts != 1 {
				t.Fatal("rejection mutated retry budget", decisions, attempts, err)
			}
		})
	}
}

func TestCandidateReviewDiagnosticRejectsWrongWriterVersion(t *testing.T) {
	for _, value := range []string{`1`, `0`, `3`, `-2`, `2.0`, `"2"`, `null`, `true`} {
		t.Run(value, func(t *testing.T) {
			body := strings.Replace(reviewDiagnosticV2Body, `"writer_version":2`, `"writer_version":`+value, 1)
			f := diagnosticReviewFixtureBody(t, adapter.ProviderGrok, nil, nil, body, 7)
			if _, err := f.db.QueueRetry(context.Background(), f.spec); err == nil {
				t.Fatal("invalid writer version authorized retry")
			}
		})
	}
}

func TestCandidateReviewDiagnosticLegacyKeepsOriginalLifecycleRules(t *testing.T) {
	for _, kind := range []string{contract.EventPrepared, contract.EventSpawned} {
		t.Run("missing_"+kind, func(t *testing.T) {
			f := diagnosticReviewFixture(t, adapter.ProviderGrok)
			if _, err := f.db.sql.Exec(`DELETE FROM runtime_events WHERE event_id=?`, "diagnostic-"+kind); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.QueueRetry(context.Background(), f.spec); err != nil {
				t.Fatal("legacy history silently required v2 proof", err)
			}
		})
	}
}

func TestCandidateReviewDiagnosticV2RejectsSessionEvidence(t *testing.T) {
	for _, name := range []string{"process", "no_process", "identity", "other_identity", "other_kind", "exit", "artifact", "before_running", "after_failed"} {
		t.Run(name, func(t *testing.T) {
			f := diagnosticReviewFixtureBody(t, adapter.ProviderClaude, nil, nil, reviewDiagnosticV2Body, 0, true)
			var raw string
			var e contract.Event
			if err := f.db.sql.QueryRow(`SELECT body_json FROM runtime_events WHERE event_id='diagnostic-session'`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(raw), &e); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "process":
				e.Process.Birth = "other"
			case "no_process":
				e.Process = nil
			case "identity":
				e.SessionID = ""
			case "other_identity":
				e.SessionID = "another-session"
			case "other_kind":
				e.SessionKind = "thread-id"
			case "exit":
				value := 0
				e.ExitCode = &value
			case "artifact":
				e.Artifact = f.event.Artifact
			case "before_running":
				e.Sequence -= 2
			case "after_failed":
				e.Sequence += 2
			}
			body, _ := json.Marshal(e)
			if _, err := f.db.sql.Exec(`UPDATE runtime_events SET body_json=? WHERE event_id=?`, string(body), e.EventID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.QueueRetry(context.Background(), f.spec); err == nil {
				t.Fatal("invalid session evidence authorized retry")
			}
		})
	}
}

func TestCandidateReviewDiagnosticV2RejectsDuplicateSession(t *testing.T) {
	f := diagnosticReviewFixtureBody(t, adapter.ProviderClaude, nil, nil, reviewDiagnosticV2Body, 0, true)
	// Preserve increasing producer sequence while adding a second session in
	// the running interval; rejection must inspect multiplicity, not ordering.
	for _, kind := range []string{contract.EventExited, contract.EventFailed} {
		var raw string
		var e contract.Event
		if err := f.db.sql.QueryRow(`SELECT body_json FROM runtime_events WHERE event_id=?`, "diagnostic-"+kind).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			t.Fatal(err)
		}
		e.Sequence++
		body, _ := json.Marshal(e)
		if _, err := f.db.sql.Exec(`UPDATE runtime_events SET sequence=?,body_json=? WHERE event_id=?`, e.Sequence, string(body), e.EventID); err != nil {
			t.Fatal(err)
		}
	}
	var raw string
	var session contract.Event
	if err := f.db.sql.QueryRow(`SELECT body_json FROM runtime_events WHERE event_id='diagnostic-session'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &session); err != nil {
		t.Fatal(err)
	}
	session.Sequence++
	session.EventID = "second-session"
	body, _ := json.Marshal(session)
	if _, err := f.db.sql.Exec(`INSERT INTO runtime_events(event_id,producer_id,sequence,task_id,attempt_id,segment_id,payload_hash,body_json,delivery_status,created_at) SELECT ?,producer_id,?,task_id,attempt_id,segment_id,payload_hash,?,delivery_status,created_at FROM runtime_events WHERE event_id='diagnostic-session'`, session.EventID, session.Sequence, string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.QueueRetry(context.Background(), f.spec); err == nil {
		t.Fatal("duplicate session authorized v2 retry")
	}
}
