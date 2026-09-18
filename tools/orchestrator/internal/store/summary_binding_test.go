package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type summaryBindingFixture struct {
	db    *DB
	token string
	grant contract.LaunchCommand
	event contract.Event
}

func newSummaryBindingFixture(t *testing.T) summaryBindingFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	receipt, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", DeliveryMode: "collect"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"}, Tasks: []TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "gen", PID: 10, Birth: "host", Executable: "/private/bin/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("private result body")
	path := filepath.Join(root, "private-result.txt")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	event := contract.Event{Version: 1, ProducerID: host, RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: grant.WorkRevision, ExecutionEpoch: 1, CommandID: grant.CommandID, EventID: "result", Sequence: 1, EventRevision: 999, ActionSlot: "body-slot-is-not-persisted-slot", Kind: contract.EventResult, PayloadHash: runtimeHash(string(body)), Artifact: &contract.ArtifactRef{Path: path, Size: int64(len(body)), SHA256: runtimeHash(string(body))}}
	if _, err = db.CommitHostEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	done := event
	done.EventID = "exited"
	done.Sequence = 2
	done.Kind = contract.EventExited
	zero := 0
	done.ExitCode = &zero
	if _, err = db.CommitHostEvent(ctx, done); err != nil {
		t.Fatal(err)
	}
	page, err := db.CollectPending(ctx, "task", "", 0, false)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("collect=%+v err=%v", page, err)
	}
	return summaryBindingFixture{db: db, token: receipt.ControlToken, grant: grant, event: page.Events[0]}
}

type projectedReviewBinding struct {
	EventID       string `json:"event_id"`
	EventRevision int64  `json:"event_revision"`
	EventHash     string `json:"event_hash"`
	ActionSlot    string `json:"action_slot"`
	WorkRevision  int    `json:"work_revision"`
}

func summaryResultBinding(t *testing.T, f summaryBindingFixture) *projectedReviewBinding {
	t.Helper()
	summary, err := f.db.SummarizeRun(context.Background(), "task", "owner", f.token)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{f.token, f.event.Artifact.Path, "private result body"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("summary leaked private content")
		}
	}
	var projected struct {
		Tasks []struct {
			ReviewableResult *projectedReviewBinding `json:"reviewable_result"`
		} `json:"tasks"`
	}
	if err = json.Unmarshal(raw, &projected); err != nil {
		t.Fatal(err)
	}
	if len(projected.Tasks) != 1 {
		t.Fatal("missing summary task")
	}
	return projected.Tasks[0].ReviewableResult
}

func TestSummaryRecoversExactReviewBindingAfterACK(t *testing.T) {
	f := newSummaryBindingFixture(t)
	ctx := context.Background()
	e := f.event
	delivery, proof, err := f.db.CollectionReceipt(ctx, "task", []contract.Event{e})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.AckDelivery(ctx, "task", delivery, "", proof, []AckDecision{{EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, Decision: "handled", CommandID: "ack"}}); err != nil {
		t.Fatal(err)
	}
	page, err := f.db.CollectPending(ctx, "task", "", 0, false)
	if err != nil || len(page.Events) != 0 {
		t.Fatalf("ACKed collection=%+v err=%v", page, err)
	}
	binding := summaryResultBinding(t, f)
	want := projectedReviewBinding{e.EventID, e.EventRevision, e.PayloadHash, e.ActionSlot, e.WorkRevision}
	if binding == nil || *binding != want {
		t.Fatalf("summary binding=%+v want=%+v", binding, want)
	}
	var deliveryState string
	var decisions int
	f.db.sql.QueryRow(`SELECT delivery_status FROM runtime_events WHERE event_id='result'`).Scan(&deliveryState)
	f.db.sql.QueryRow(`SELECT COUNT(*) FROM review_decisions`).Scan(&decisions)
	if deliveryState != "acked" || decisions != 0 {
		t.Fatal("summary changed delivery or made review decision")
	}
	if status, err := f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: binding.WorkRevision, EventID: binding.EventID, EventRevision: binding.EventRevision, EventHash: binding.EventHash, ActionSlot: binding.ActionSlot, Decision: "accept", CommandID: "explicit-accept"}); err != nil || status != "accepted" {
		t.Fatalf("accept=%s %v", status, err)
	}
	if summaryResultBinding(t, f) != nil {
		t.Fatal("accepted result still presented as reviewable")
	}
}

func TestSummaryReviewBindingFiltersUnreviewableEvidence(t *testing.T) {
	for _, scenario := range []string{"pending", "old_revision", "old_segment", "old_attempt", "new_attempt_without_segment", "active", "unknown", "not_result", "no_artifact", "wrong_host", "worker_report", "wrong_owner"} {
		t.Run(scenario, func(t *testing.T) {
			f := newSummaryBindingFixture(t)
			e := f.event
			mutate := func(query string, args ...any) {
				t.Helper()
				if _, err := f.db.sql.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "old_revision":
				mutate(`UPDATE task_runtime SET work_revision=2 WHERE task_id='task'`)
			case "old_segment":
				mutate(`INSERT INTO segments(id,attempt_id,segment_no,status,created_at) VALUES('new-segment',?,2,'exited','')`, e.AttemptID)
			case "new_attempt_without_segment":
				mutate(`INSERT INTO attempts(id,task_id,attempt_no,status,created_at) VALUES('new-attempt','task',2,'running','')`)
			case "old_attempt":
				mutate(`INSERT INTO attempts(id,task_id,attempt_no,status,created_at) VALUES('new-attempt','task',2,'running','')`)
				mutate(`INSERT INTO segments(id,attempt_id,segment_no,status,created_at) VALUES('new-segment','new-attempt',1,'exited','')`)
			case "active", "unknown":
				state := "running"
				if scenario == "unknown" {
					state = "unknown"
				}
				mutate(`UPDATE segment_runtime SET status=? WHERE segment_id=?`, state, e.SegmentID)
			case "not_result":
				e.Kind = contract.EventFailed
				b, _ := json.Marshal(e)
				mutate(`UPDATE runtime_events SET body_json=? WHERE event_id='result'`, string(b))
			case "no_artifact":
				e.Artifact = nil
				b, _ := json.Marshal(e)
				mutate(`UPDATE runtime_events SET body_json=? WHERE event_id='result'`, string(b))
			case "wrong_host":
				mutate(`INSERT INTO host_launches(id,token_hash,origin_context_id,host_generation,executable,status,created_at) VALUES('other-launch','hash','other-origin','other-generation','/private/bin/orchestrator','ready','')`)
				mutate(`INSERT INTO runtime_hosts(id,launch_id,origin_context_id,host_generation,pid,birth,executable,coordinator_epoch,status,updated_at) VALUES('not-source-host','other-launch','other-origin','other-generation',20,'birth','/private/bin/orchestrator',1,'ready','')`)
				mutate(`UPDATE runtime_events SET producer_id='not-source-host' WHERE event_id='result'`)
			case "worker_report":
				b, _ := json.Marshal(e)
				mutate(`DELETE FROM runtime_events WHERE event_id='result'`)
				mutate(`INSERT INTO report_events(event_id,capability_id,sequence,kind,payload_hash,body_json,event_revision,action_slot,delivery_status,accounted_bytes,created_at) VALUES('worker-result',?,1,'result',?,?,1,'worker-slot','acked',0,'')`, f.grant.ReportCapabilityID, e.PayloadHash, string(b))
			case "wrong_owner":
				if _, err := f.db.SummarizeRun(context.Background(), "task", "other-owner", f.token); err == nil {
					t.Fatal("wrong owner received binding")
				}
				return
			}
			binding := summaryResultBinding(t, f)
			if scenario == "pending" {
				if binding == nil || binding.EventID != e.EventID {
					t.Fatal("pending result binding missing")
				}
			} else if binding != nil {
				t.Fatalf("unreviewable binding=%+v", binding)
			}
		})
	}
}
