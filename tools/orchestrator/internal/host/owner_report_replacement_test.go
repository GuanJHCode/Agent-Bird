package host

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestOwnerExecutionReportReplacementRetainsAcceptedCapability(t *testing.T) {
	testOwnerReportReplacement(t, false)
}

func TestOwnerExecutionReportReplacementStartsOriginalWorker(t *testing.T) {
	testOwnerReportReplacement(t, true)
}

func testOwnerReportReplacement(t *testing.T, physical bool) {
	cfg, first, key := ownerExecutionFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	registrations := make(chan contract.ReportCapabilityRegistration, 8)
	var oldCalls atomic.Int32
	cfg.rpc = func(ctx context.Context, revision uint64, kind, id string, body any) (contract.OwnerRPCReply, error) {
		reply := contract.OwnerRPCReply{Version: 1, OwnerID: first.OwnerID, CoordinatorEpoch: first.CoordinatorEpoch, RequestID: id}
		var result any
		switch kind {
		case "report_capability":
			registration := body.(contract.ReportCapabilityRegistration)
			registrations <- registration
			if revision == 1 {
				if oldCalls.Add(1) == 1 {
					close(entered)
				}
				select {
				case <-release:
				case <-ctx.Done():
					return contract.OwnerRPCReply{}, ctx.Err()
				}
				reply.Error = "original_owner_rejected"
				return reply, errors.New(reply.Error)
			}
			result = contract.DurableAck{Status: "durable", EventID: registration.CapabilityID, PayloadHash: registration.TokenHash}
		case "event":
			event := body.(contract.Event)
			result = contract.DurableAck{Status: "durable", ProducerID: event.ProducerID, EventID: event.EventID, PayloadHash: event.PayloadHash, AckedThrough: event.Sequence}
		case "applied":
			result = map[string]string{"status": "applied"}
		case "activate":
			result = map[string]string{"status": "active"}
		default:
			t.Error("unexpected RPC", kind)
		}
		reply.Body, _ = json.Marshal(result)
		return reply, nil
	}
	cfg.invocation = func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
		// Keep the real actor alive long enough for its original birth capture.
		return launchInvocation{args: []string{"/bin/sleep", "0.5"}}, nil
	}
	e, err := newOwnerExecution(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.run.cancel(context.Canceled)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = e.reconcile(ctx, first); err != nil {
		t.Fatal(err)
	}
	grant := contract.LaunchCommand{CommandID: "command", ReservationID: "reservation", RunID: "run", HostID: "producer", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1, ExecutionEpoch: first.CoordinatorEpoch, GrantedActiveMS: 2000, ReportCapabilityID: "capability"}
	done := make(chan error, 1)
	if physical {
		body, _ := json.Marshal(grant)
		command, err := contract.SignOwnerCommand(contract.OwnerCommand{Version: 1, Permit: first, Kind: "launch", RequestID: grant.CommandID, Body: body}, key)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(command)
		if err = e.command(raw); err != nil {
			t.Fatal(err)
		}
		go func() { e.run.workers.Wait(); done <- nil }()
	} else {
		go func() {
			path, err := e.prepareReport(ctx, grant)
			if err == nil && path == "" {
				err = errors.New("missing acknowledged capability")
			}
			done <- err
		}()
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("old registration never entered")
	}
	e.pausePublication() // before remote Attach commits
	next := first
	next.ExpectedControlRevision, next.ControlRevision = 1, 2
	next.OldHost = &first.NewHost
	next.NewHost.PID++
	next.RequestID, next.PermitID = "replacement", "permit-2"
	next, err = contract.SignOwnerControlPermit(next, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.reconcile(ctx, next); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err = <-done:
		if err != nil {
			t.Fatal("accepted report was failed by obsolete rejection", err)
		}
	case <-ctx.Done():
		t.Fatal("report did not resume")
	}
	close(registrations)
	baseline := <-registrations
	count := 1
	for registration := range registrations {
		count++
		if registration != baseline {
			t.Fatal("replacement rotated report authority")
		}
	}
	if count != 3 {
		t.Fatal("expected old request, registry replay, and resumed preparation", count)
	}
	if physical {
		spool, err := cfg.host.spool(store.Attempt{ID: grant.AttemptID, SegmentID: grant.SegmentID})
		if err != nil {
			t.Fatal(err)
		}
		events, err := spool.Read()
		if err != nil {
			t.Fatal(err)
		}
		exited := 0
		for _, event := range events {
			if event.Kind == contract.EventLaunchRejected {
				t.Fatal("replacement incorrectly failed accepted claim")
			}
			if event.Kind == contract.EventExited && event.ExitCode != nil && *event.ExitCode == 0 {
				exited++
			}
		}
		if exited != 1 {
			t.Fatal("original worker did not produce one real Wait=0", events)
		}
	}
}
