package host

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestOwnerCompletedRetirementCleanupKeepsErrorsAndPendingEvents(t *testing.T) {
	for _, scenario := range []string{"completed", "coordinator_exit", "untyped_text", "joined_error", "pending_event", "worker_join_failure"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, permit, key := ownerExecutionFixture(t)
			original := cfg.rpc
			var registrations, events atomic.Int32
			registrationFailure := errors.New("retired_host_registration_conflict")
			eventFailure := errors.New("unacknowledged_event_refused")
			joinFailure := errors.New("original_worker_wait_failed")
			cfg.rpc = func(ctx context.Context, revision uint64, kind, id string, body any) (contract.OwnerRPCReply, error) {
				if kind == "report_capability" {
					registrations.Add(1)
					return contract.OwnerRPCReply{}, registrationFailure
				}
				if kind == "event" {
					events.Add(1)
					return contract.OwnerRPCReply{}, eventFailure
				}
				return original(ctx, revision, kind, id, body)
			}
			e, err := newOwnerExecution(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer e.run.cancel(context.Canceled)
			if err = e.reconcile(context.Background(), permit); err != nil {
				t.Fatal(err)
			}
			registration := contract.ReportCapabilityRegistration{CapabilityID: "finished-cap", TokenHash: strings.Repeat("a", 64), ProducerID: cfg.binding.Scope.ProducerID, ExecutionEpoch: permit.CoordinatorEpoch}
			if err = e.run.reports.remember(registration); err != nil {
				t.Fatal(err)
			}
			reason := "host_retired"
			if scenario == "coordinator_exit" {
				reason = "coordinator_exit"
			}
			raw, _ := json.Marshal(contract.OwnerTerminal{Reason: reason})
			command, err := contract.SignOwnerCommand(contract.OwnerCommand{Version: 1, Permit: permit, Kind: "terminal", RequestID: "retirement", Body: raw}, key)
			if err != nil {
				t.Fatal(err)
			}
			signed, _ := json.Marshal(command)
			cause := e.command(signed)
			if ownerCommandDisposition(cause) != ownerCommandTerminal {
				t.Fatal("fixture lacks authenticated terminal", cause)
			}
			if scenario == "untyped_text" {
				cause = errors.New("host_retired")
			}
			if scenario == "joined_error" {
				cause = errors.Join(cause, errors.New("independent_failure"))
			}
			if scenario == "worker_join_failure" {
				e.admissionFailure = joinFailure
			}
			var pendingSequence int64
			if scenario == "pending_event" {
				a := store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}
				spool, err := cfg.host.spool(a)
				if err != nil {
					t.Fatal(err)
				}
				event, err := cfg.host.appendPhaseEvent(spool, "run", "task", a, launchMetadata{commandID: "command", workRevision: 1, executionEpoch: permit.CoordinatorEpoch}, contract.EventPrepared, hashText("unacknowledged"), nil, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				pendingSequence = event.Sequence
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			lifecycle := newOwnerRuntimeLifecycle(e, cancel)
			err = lifecycle.cleanup(cause, nil)
			if context.Cause(ctx) != cause {
				t.Fatal("cleanup lost its original internal cause", context.Cause(ctx))
			}
			switch scenario {
			case "completed":
				if err != nil || registrations.Load() != 0 {
					t.Fatal("completed retirement replayed stale capability", err, registrations.Load())
				}
			case "coordinator_exit", "untyped_text", "joined_error":
				if !errors.Is(err, registrationFailure) || registrations.Load() != 1 {
					t.Fatal("non-completed failure suppressed report replay", err, registrations.Load())
				}
			case "worker_join_failure":
				if !errors.Is(err, joinFailure) || registrations.Load() != 0 {
					t.Fatal("completed retirement lost original join failure", err, registrations.Load())
				}
			case "pending_event":
				if !errors.Is(err, eventFailure) || events.Load() != 1 || registrations.Load() != 0 {
					t.Fatal("retirement dropped pending event failure", err, events.Load(), registrations.Load())
				}
				a := store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}
				spool, err := cfg.host.spool(a)
				if err != nil {
					t.Fatal(err)
				}
				acked, err := spool.AckedThrough()
				if err != nil || acked >= pendingSequence {
					t.Fatal("rejected event was falsely acknowledged", acked, err)
				}
				retained, err := spool.ReadAfter(acked)
				if err != nil || len(retained) != 1 || retained[0].Sequence != pendingSequence {
					t.Fatal("unacknowledged event not retained", retained, err)
				}
			}
		})
	}
}
