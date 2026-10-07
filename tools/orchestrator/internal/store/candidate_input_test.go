package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Removing the durable accepted-event handoff must prevent the consumer from
// receiving an unbound path; owner acceptance alone cannot prove process exit.
func TestCandidateInputBindsAcceptedHostResultAndReplay(t *testing.T) {
	for _, exited := range []bool{false, true} {
		t.Run(map[bool]string{false: "still_running", true: "exited"}[exited], func(t *testing.T) {
			ctx := context.Background()
			db, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			plan, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 7, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"}, Tasks: []TaskSpec{
				{ID: "edit", RunID: "run", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"claude-code","candidate_workspace":{"version":1}}`)},
				{ID: "check", RunID: "run", Dependencies: []string{"edit"}, MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"kind":"candidate","candidate_action":{"version":1,"operation":"validate","command":["/usr/bin/true"],"source_task":"edit"}}`)},
			}})
			if err != nil {
				t.Fatal(err)
			}
			hostID, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "gen", PID: 10, Birth: "host", Executable: "/private/bin/orchestrator"}, 1)
			if err != nil {
				t.Fatal(err)
			}
			grant, err := db.ClaimReady(ctx, hostID, 1, 2)
			if err != nil {
				t.Fatal(err)
			}
			body := []byte(`{"candidate":"immutable"}`)
			zero := 0
			e := contract.Event{Version: 1, ProducerID: hostID, EventID: "edit-result", RunID: "run", TaskID: "edit", AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: grant.CommandID, Sequence: 1, Kind: contract.EventResult, PayloadHash: runtimeHash(string(body)), Artifact: managedHostArtifactForTest(t, db, grant, "terminal", 0, body)}
			if _, err = db.CommitHostEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
			if exited {
				done := e
				done.EventID = "edit-exited"
				done.Sequence = 2
				done.Kind = contract.EventExited
				done.ExitCode = &zero
				if _, err = db.CommitHostEvent(ctx, done); err != nil {
					t.Fatal(err)
				}
			}
			page, err := db.CollectPending(ctx, "edit", "", 0, false)
			if err != nil || len(page.Events) != 1 {
				t.Fatalf("page=%+v err=%v", page, err)
			}
			r := page.Events[0]
			_, err = db.ReviewResult(ctx, ReviewSpec{TaskID: "edit", WorkRevision: 1, EventID: r.EventID, EventRevision: r.EventRevision, EventHash: r.PayloadHash, ActionSlot: r.ActionSlot, Decision: "accept", CommandID: "accept-edit"})
			if !exited {
				if err == nil {
					t.Fatal("accepted result before source exit")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			next, err := db.ClaimReady(ctx, hostID, 1, 2)
			if err != nil {
				t.Fatal(err)
			}
			check := func(c contract.LaunchCommand) {
				t.Helper()
				raw, _ := json.Marshal(c)
				var wire struct {
					Input struct {
						Event             contract.Event `json:"event"`
						DecisionCommandID string         `json:"decision_command_id"`
					} `json:"input"`
				}
				if err = json.Unmarshal(raw, &wire); err != nil {
					t.Fatal(err)
				}
				if wire.Input.Event.EventID != "edit-result" || wire.Input.Event.EventRevision != 1 || wire.Input.Event.Artifact == nil || wire.Input.Event.Artifact.SHA256 != runtimeHash(string(body)) || wire.Input.DecisionCommandID != "accept-edit" {
					t.Fatalf("grant lost accepted candidate evidence: %s", raw)
				}
			}
			check(next)
			legacyBody, _ := json.Marshal(struct {
				Plan          int
				Payload       json.RawMessage
				Input         *contract.AcceptedInput
				HostArtifacts *contract.HostArtifactGrant
			}{next.PlanRevision, next.AdapterPayload, next.Input, next.HostArtifacts})
			legacyHash := runtimeHash(next.RunID + "\x00" + next.TaskID + "\x00" + next.AttemptID + "\x00" + next.SegmentID + "\x00" + next.SlotToken + "\x00" + string(legacyBody))
			if next.LaunchIntentHash != legacyHash {
				t.Fatal("new candidate intent omitted or changed Host authority")
			}
			pending, err := db.PendingLaunches(ctx, hostID, 1)
			if err != nil || len(pending) != 1 {
				t.Fatalf("pending=%d err=%v", len(pending), err)
			}
			check(pending[0])
			if pending[0].LaunchIntentHash != next.LaunchIntentHash {
				t.Fatal("replay changed immutable input")
			}
		})
	}
}

func TestCandidatePlanRejectsInvalidInputBeforeReservingHost(t *testing.T) {
	for _, kind := range []string{"undeclared_source", "automatic_accept"} {
		t.Run(kind, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			source := TaskSpec{ID: "edit", RunID: "run", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"claude-code","profile":{"version":1,"role":"implementer","permission":"workspace-write"},"candidate_workspace":{"version":1}}`)}
			check := TaskSpec{ID: "check", RunID: "run", MaxAttempts: 1, Dependencies: []string{"edit"}, AdapterPayload: json.RawMessage(`{"kind":"candidate","candidate_action":{"version":1,"operation":"validate","command":["/usr/bin/true"],"source_task":"edit"}}`)}
			if kind == "undeclared_source" {
				check.AdapterPayload = json.RawMessage(`{"kind":"candidate","candidate_action":{"version":1,"operation":"validate","command":["/usr/bin/true"],"source_task":"unrelated"}}`)
			} else {
				source.CompletionPolicy = "artifact"
				source.ExpectedArtifactSHA256 = runtimeHash("artifact")
			}
			_, err = db.SubmitPlan(context.Background(), PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"}, Tasks: []TaskSpec{source, check}})
			if err == nil {
				t.Fatal("poisoned ready task admitted into source Host")
			}
		})
	}
}

// A second dependency is evidence, not merely a scheduling gate. Until the
// action explicitly opts into the v1 dependency-input vector, admission must
// refuse it instead of silently binding only source_task.
func TestCandidatePlanRejectsUnversionedJoinBeforeReservingHost(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tasks := []TaskSpec{
		{ID: "edit", RunID: "run", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"claude-code","candidate_workspace":{"version":1}}`)},
		{ID: "side", RunID: "run", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"claude-code","candidate_workspace":{"version":1}}`)},
		{ID: "validate", RunID: "run", Dependencies: []string{"edit"}, MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"kind":"candidate","candidate_action":{"version":1,"operation":"validate","command":["/usr/bin/true"],"source_task":"edit"}}`)},
		{ID: "review", RunID: "run", Dependencies: []string{"validate", "side"}, MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"claude-code","profile":{"version":1,"role":"reviewer","permission":"read-only"},"candidate_action":{"version":1,"operation":"review","source_task":"validate"}}`)},
	}
	if _, err = db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"}, Tasks: tasks}); err == nil || err.Error() != "candidate_dependency_inputs_required" {
		t.Fatalf("unversioned join admitted: %v", err)
	}
	var count int
	if err = db.sql.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial plan rows=%d err=%v", count, err)
	}
}
