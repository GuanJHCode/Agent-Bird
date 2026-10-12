package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestDependencyVectorReviewRetryCopiesOriginalCapture(t *testing.T) {
	for _, kind := range []string{"replay", "non_source_drift_before_queue", "non_source_drift_after_queue", "missing_source_capture"} {
		t.Run(kind, func(t *testing.T) {
			f := newReviewRetryFixtureWithVector(t, adapter.ProviderClaude, contract.EventStopped, nil, true)
			ctx := context.Background()
			if kind == "non_source_drift_before_queue" {
				if _, err := f.db.sql.Exec(`UPDATE review_decisions SET command_id='changed' WHERE task_id='edit'`); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.QueueRetry(ctx, f.spec); err == nil {
					t.Fatal("retry decision ignored non-source drift")
				}
				return
			}
			if _, err := f.db.QueueRetry(ctx, f.spec); err != nil {
				t.Fatal(err)
			}
			if kind == "non_source_drift_after_queue" {
				if _, err := f.db.sql.Exec(`UPDATE task_runtime SET adapter_payload='{}' WHERE task_id='edit'`); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "missing_source_capture" {
				if _, err := f.db.sql.Exec(`DROP TRIGGER dependency_inputs_pair_update; UPDATE segment_runtime SET dependency_inputs_json=NULL,dependency_inputs_sha256=NULL WHERE segment_id=?`, f.grant.SegmentID); err != nil {
					t.Fatal(err)
				}
			}
			c, err := f.db.ClaimReady(ctx, f.host, 1, 2)
			if kind != "replay" {
				if err == nil {
					t.Fatal("retry recaptured changed source")
				}
				var count int
				f.db.sql.QueryRow(`SELECT COUNT(*) FROM attempts WHERE task_id='review'`).Scan(&count)
				if count != 1 {
					t.Fatal("failed retry retained attempt")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			old, _ := json.Marshal(f.grant.DependencyInputs)
			next, _ := json.Marshal(c.DependencyInputs)
			if string(old) != string(next) {
				t.Fatal("retry changed captured vector")
			}
			var source, copy, hash, copyHash string
			if err = f.db.sql.QueryRow(`SELECT dependency_inputs_json,dependency_inputs_sha256 FROM segment_runtime WHERE segment_id=?`, f.grant.SegmentID).Scan(&source, &hash); err != nil {
				t.Fatal(err)
			}
			if err = f.db.sql.QueryRow(`SELECT dependency_inputs_json,dependency_inputs_sha256 FROM segment_runtime WHERE segment_id=?`, c.SegmentID).Scan(&copy, &copyHash); err != nil {
				t.Fatal(err)
			}
			if source != copy || hash != copyHash {
				t.Fatal("retry did not copy original stored bytes")
			}
			pending, err := f.db.PendingLaunches(ctx, f.host, 1)
			if err != nil || len(pending) != 1 || pending[0].LaunchIntentHash != c.LaunchIntentHash {
				t.Fatalf("retry replay %v", err)
			}
			var decision string
			f.db.sql.QueryRow(`SELECT revision_payload FROM retry_decisions WHERE action_slot=?`, f.spec.ActionSlot).Scan(&decision)
			saved, err := parseReviewRetry(decision)
			if err != nil || saved.DependencyInputsSHA256 != hash {
				t.Fatalf("decision omitted whole input vector: %s %v", decision, err)
			}
		})
	}
}

func TestDependencyVectorBranchedReworkRemainsExplicitlyUnsupported(t *testing.T) {
	f := newReworkFixtureWithTasks(t, nil, func(tasks []TaskSpec) {
		for i := range tasks {
			if tasks[i].ID == "review" {
				p, err := adapter.DecodeInvocationPayload(tasks[i].AdapterPayload)
				if err != nil {
					t.Fatal(err)
				}
				p.CandidateAction.DependencyInputVersion = 1
				tasks[i].Dependencies = []string{"validate", "edit"}
				tasks[i].AdapterPayload, err = json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	})
	_, err := f.db.QueueRework(context.Background(), f.spec)
	if err == nil || err.Error() != "rework_chain_unsupported" {
		t.Fatalf("join silently entered linear rework: %v", err)
	}
	var changed int
	f.db.sql.QueryRow(`SELECT COUNT(*) FROM task_runtime WHERE work_revision<>1`).Scan(&changed)
	if changed != 0 {
		t.Fatal("rejected join changed revisions")
	}
}
