package store

import (
	"context"
	"encoding/json"
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
	f := newReviewRetryFixtureBound(t, provider, "", nil, true, true, plan, bind)
	g := f.grant
	// testing.TempDir subdirectories do not establish the runtime 0700 contract.
	if err := os.Chmod(filepath.Dir(f.db.path), 0700); err != nil {
		t.Fatal(err)
	}
	ref := managedHostArtifactForTest(t, f.db, g, "terminal", 0, []byte(reviewDiagnosticBody))
	e := contract.Event{Version: 1, ProducerID: f.host, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, ExecutionEpoch: 1, CommandID: g.CommandID, Process: &contract.ProcessIdentity{PID: 300, PGID: 300, Birth: "review-worker"}, Sequence: 4}
	for _, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventFailed, contract.EventExited} {
		e.Sequence++
		e.Kind, e.EventID, e.PayloadHash = kind, "diagnostic-"+kind, runtimeHash(kind)
		if kind == contract.EventFailed || kind == contract.EventExited {
			exit := 7
			e.ExitCode, e.ActiveMS = &exit, 1
			e.Artifact = ref
			e.PayloadHash = e.Artifact.SHA256
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
