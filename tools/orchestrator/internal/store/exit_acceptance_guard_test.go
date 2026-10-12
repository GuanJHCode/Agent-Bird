package store

// Exit-code acceptance guard (task C restoration, batch 2). A durable result
// must only settle as result_ready when the closing exited event carries an
// explicit zero exit code; missing or non-zero exits stay diagnostic history
// that can never be accepted. Expectations here are the contract text of
// docs/runtime-protocols.md ("进程退出 0 不自动满足" plus the C ruling that
// only an explicit exit 0 may enter result_ready), asserted independently of
// the implementation branch.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func exitPtr(value int) *int { return &value }

func TestResultExitGuardRequiresExplicitZeroExit(t *testing.T) {
	for _, testCase := range []struct {
		name string
		exit *int
	}{
		{name: "missing_exit_code", exit: nil},
		{name: "nonzero_exit_code", exit: exitPtr(17)},
		{name: "negative_exit_signal", exit: exitPtr(-9)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db, grant := quotaFixture(t)
			ctx := context.Background()
			result := contract.Event{Version: 1, ProducerID: grant.HostID, EventID: "guard-result", RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: grant.WorkRevision, ExecutionEpoch: 1, CommandID: grant.CommandID, Sequence: 1, Kind: contract.EventResult, PayloadHash: strings.Repeat("a", 64)}
			result.Artifact = managedHostArtifactForTest(t, db, grant, "terminal", 0, []byte("incomplete"))
			result.PayloadHash = result.Artifact.SHA256
			if _, err := db.CommitHostEvent(ctx, result); err != nil {
				t.Fatal(err)
			}
			exited := result
			exited.EventID, exited.Sequence, exited.Kind, exited.PayloadHash, exited.ExitCode = "guard-exited", 2, contract.EventExited, strings.Repeat("b", 64), testCase.exit
			if _, err := db.CommitHostEvent(ctx, exited); err != nil {
				t.Fatal(err)
			}
			var status string
			if err := db.sql.QueryRow(`SELECT status FROM tasks WHERE id='task'`).Scan(&status); err != nil || status != "failed" {
				t.Fatalf("unusable exit settled as %q: %v", status, err)
			}
			_, err := db.ReviewResult(ctx, ReviewSpec{TaskID: grant.TaskID, WorkRevision: grant.WorkRevision, EventID: result.EventID, EventRevision: 1, EventHash: result.PayloadHash, ActionSlot: "action-" + runtimeHash(result.ProducerID + "\x00" + result.EventID)[:32], Decision: "accept", CommandID: "accept-unusable"})
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("unusable exit authorized acceptance: %v", err)
			}
			// The diagnostic result and artifact body must survive refusal.
			var body string
			if err := db.sql.QueryRow(`SELECT body_json FROM runtime_events WHERE event_id='guard-result'`).Scan(&body); err != nil {
				t.Fatal(err)
			}
			var retained contract.Event
			if err := json.Unmarshal([]byte(body), &retained); err != nil || retained.Artifact == nil || !reflect.DeepEqual(retained.Artifact, result.Artifact) {
				t.Fatalf("diagnostic result lost: %+v %v", retained, err)
			}
		})
	}
	t.Run("explicit_zero_exit_settles", func(t *testing.T) {
		db, grant := quotaFixture(t)
		ctx := context.Background()
		result := contract.Event{Version: 1, ProducerID: grant.HostID, EventID: "ok-result", RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: grant.WorkRevision, ExecutionEpoch: 1, CommandID: grant.CommandID, Sequence: 1, Kind: contract.EventResult, PayloadHash: strings.Repeat("a", 64)}
		if _, err := db.CommitHostEvent(ctx, result); err != nil {
			t.Fatal(err)
		}
		exited := result
		exited.EventID, exited.Sequence, exited.Kind, exited.PayloadHash, exited.ExitCode, exited.ActiveMS = "ok-exited", 2, contract.EventExited, strings.Repeat("b", 64), exitPtr(0), 500
		if _, err := db.CommitHostEvent(ctx, exited); err != nil {
			t.Fatal(err)
		}
		var status string
		if err := db.sql.QueryRow(`SELECT status FROM tasks WHERE id='task'`).Scan(&status); err != nil || status != "result_ready" {
			t.Fatalf("explicit zero exit must settle result_ready: %q %v", status, err)
		}
	})
}
