package host

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestCoreSourcePreStartFailuresKeepNoStartAndUnknownDistinct(t *testing.T) {
	for _, mode := range []string{"report_directory_unsafe", "invocation_tree_unknown", "invocation_integration_uncertain"} {
		t.Run(mode, func(t *testing.T) {
			h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "producer")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			source := &livingSource{host: h, ctx: ctx, cancel: cancel, working: map[string]bool{}, cfg: SourceConfig{SocketPath: "/private/socket", InvocationProvider: func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
				switch mode {
				case "invocation_tree_unknown":
					return nil, process.ErrProcessTreeUnknown
				case "invocation_integration_uncertain":
					return nil, gitops.ErrIntegrationUncertain
				default:
					return launchInvocation{args: []string{"/usr/bin/true"}}, nil
				}
			}}}
			client, peer := net.Pipe()
			defer client.Close()
			defer peer.Close()
			session := &sourceSession{conn: client, epoch: 1}
			session.initialize()
			source.connections.set(session)
			grant := contract.LaunchCommand{CommandID: "command", ReservationID: "reservation", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, GrantedActiveMS: 5000}
			want := contract.EventUnknown
			if mode == "report_directory_unsafe" {
				grant.ReportCapabilityID = "cap"
				if err = os.MkdirAll(filepath.Join(h.spoolRoot, ".reports", "cap"), 0755); err != nil {
					t.Fatal(err)
				}
				want = contract.EventLaunchRejected
			}
			c02ProcessLaunchThroughReceive(t, source, grant, 1)
			source.workers.Wait()
			events, err := h.Collect(context.Background(), store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID})
			if err != nil || len(events) != 1 || events[0].Kind != want || len(h.reservations) != 0 || context.Cause(ctx) != nil {
				t.Fatalf("pre-Start failure canceled Host or invented process/settlement evidence: %+v err=%v cause=%v want=%s", events, err, context.Cause(ctx), want)
			}
		})
	}
}

func TestCoreLivingSourceDuplicateKeepsNoStartOrStartingUncertainty(t *testing.T) {
	for _, mode := range []string{"launch_rejected", "starting"} {
		t.Run(mode, func(t *testing.T) {
			h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "producer")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			var calls atomic.Int32
			source := &livingSource{host: h, ctx: ctx, cancel: cancel, working: map[string]bool{}, cfg: SourceConfig{InvocationProvider: func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
				calls.Add(1)
				return nil, errors.New("invalid_json")
			}}}
			grant := contract.LaunchCommand{CommandID: "command", ReservationID: "reservation", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, GrantedActiveMS: 5000}
			a := store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID}
			if mode == "launch_rejected" {
				c02ProcessLaunchThroughReceive(t, source, grant, 1)
				source.workers.Wait()
			} else {
				if _, err = h.acceptGrant(grant); err != nil {
					t.Fatal(err)
				}
				s, err := h.spool(a)
				if err != nil {
					t.Fatal(err)
				}
				if err = s.WriteMetadata("launch.json", map[string]any{"phase": "starting"}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := h.Collect(ctx, a)
			if err != nil {
				t.Fatal(err)
			}
			c02ProcessLaunchThroughReceive(t, source, grant, 2)
			source.workers.Wait()
			after, err := h.Collect(ctx, a)
			if err != nil {
				t.Fatal(err)
			}
			kind := contract.EventUnknown
			wantCalls := int32(0)
			if mode == "launch_rejected" {
				kind, wantCalls = contract.EventLaunchRejected, 1
				old, _ := json.Marshal(before)
				current, _ := json.Marshal(after)
				if string(old) != string(current) {
					t.Fatal("processed no-start duplicate changed durable evidence")
				}
			}
			if len(after) != 1 || after[0].Kind != kind || calls.Load() != wantCalls || ctx.Err() != nil || len(h.reservations) != 0 {
				t.Fatalf("duplicate invoked/spawned or reclassified: events=%+v calls=%d cause=%v", after, calls.Load(), context.Cause(ctx))
			}
		})
	}
}

func TestCoreUncommittedReportRegistrationReplaysAfterRealLaunchRejection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	sourceCtx, cancelSource := context.WithCancel(ctx)
	defer cancelSource()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hello := c02SourceIdentity(t)
	plan, err := db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: hello.OriginContextID, OriginPID: hello.OriginPID, OriginBirth: hello.OriginBirth}, Host: store.HostLaunchSpec{OriginContextID: hello.OriginContextID, HostGeneration: hello.HostGeneration, Executable: hello.Executable}, Tasks: []store.TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 1, MaxActiveMS: 800, AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	hello.LaunchID, hello.LaunchToken = plan.LaunchID, plan.LaunchToken
	producer, err := db.RegisterHost(ctx, hello, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, producer, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewIPC(filepath.Join(root, "spool"), producer)
	if err != nil {
		t.Fatal(err)
	}
	socket, listener := c02SourceSocket(t)
	rejected := make(chan struct{})
	observed := make(chan struct{})
	go func() {
		defer close(observed)
		for {
			events, err := h.Collect(ctx, store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID})
			if err == nil && len(events) == 1 && events[0].Kind == contract.EventLaunchRejected {
				close(rejected)
				return
			}
			select {
			case <-h.eventReady:
			case <-ctx.Done():
				return
			}
		}
	}()
	var original contract.ReportCapabilityRegistration
	var originalFile []byte
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- func() error {
			for epoch := uint64(1); epoch <= 2; epoch++ {
				conn, err := listener.Accept()
				if err != nil {
					return err
				}
				defer conn.Close()
				deadline, _ := ctx.Deadline()
				_ = conn.SetDeadline(deadline)
				frame, err := ipc.Read(conn)
				if err != nil || frame.Kind != ipc.KindHostHello {
					return errors.New("missing reconnect hello")
				}
				if epoch == 2 {
					if err = db.BeginCoordinatorEpoch(ctx, epoch); err != nil {
						return err
					}
					if _, err = db.RegisterHost(ctx, hello, epoch); err != nil {
						return err
					}
				}
				if err = db.BeginHostReconcile(ctx, producer); err != nil {
					return err
				}
				body, _ := json.Marshal(contract.HostReady{HostID: producer, CoordinatorEpoch: epoch})
				if err = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindHostReady, RequestID: frame.RequestID, Epoch: epoch, Payload: body}); err != nil {
					return err
				}
				if epoch == 1 {
					frame, err = ipc.Read(conn)
					if err != nil || frame.Kind != ipc.KindHostReconciled {
						return errors.New("missing original reconcile")
					}
					if err = db.FinishHostReconcile(ctx, producer); err != nil {
						return err
					}
					body, _ = json.Marshal(grant)
					if err = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindLaunch, RequestID: grant.CommandID, Epoch: epoch, Payload: body}); err != nil {
						return err
					}
					frame, err = ipc.Read(conn)
					if err != nil || frame.Kind != ipc.KindReportCapability || json.Unmarshal(frame.Payload, &original) != nil {
						return errors.New("original remembered registration missing")
					}
					originalFile, err = os.ReadFile(filepath.Join(original.CapabilityDir, "capability.json"))
					if err != nil {
						return err
					}
					// No Store registration call: disconnect before DB commit/ACK.
					_ = conn.Close()
					continue
				}
				frame, err = ipc.Read(conn)
				var event contract.Event
				if err != nil || frame.Kind != ipc.KindEvent || json.Unmarshal(frame.Payload, &event) != nil || event.Kind != contract.EventLaunchRejected {
					return errors.New("durable rejection did not replay before report binding")
				}
				ack, err := db.CommitHostEvent(ctx, event)
				if err != nil {
					return err
				}
				var status, token, directory string
				if err = db.SQL().QueryRow(`SELECT status,token_hash,capability_dir FROM report_capabilities WHERE capability_id=?`, original.CapabilityID).Scan(&status, &token, &directory); err != nil || status != "revoked" || token != "" || directory != "" {
					return errors.New("real rejected consumer did not revoke an empty pending binding")
				}
				body, _ = json.Marshal(ack)
				if err = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindDurableAck, RequestID: frame.RequestID, Epoch: epoch, Payload: body}); err != nil {
					return err
				}
				frame, err = ipc.Read(conn)
				var replay contract.ReportCapabilityRegistration
				if err != nil || frame.Kind != ipc.KindReportCapability || json.Unmarshal(frame.Payload, &replay) != nil || replay.ExecutionEpoch != epoch || reportBinding(replay) != reportBinding(original) {
					return errors.New("revoked replay changed the original remembered binding")
				}
				ack, err = db.RegisterReportCapability(ctx, replay)
				if err != nil {
					return err
				}
				body, _ = json.Marshal(ack)
				if err = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindDurableAck, RequestID: frame.RequestID, Epoch: epoch, Payload: body}); err != nil {
					return err
				}
				frame, err = ipc.Read(conn)
				var state contract.HostReconciled
				if err != nil || frame.Kind != ipc.KindHostReconciled || json.Unmarshal(frame.Payload, &state) != nil || state.PublishedThrough != 1 {
					return errors.New("reconnect did not complete after revoked binding ACK")
				}
				if err = db.FinishHostReconcile(ctx, producer); err != nil {
					return err
				}
				cancelSource()
			}
			return nil
		}()
	}()
	var calls atomic.Int32
	err = runLivingSource(sourceCtx, SourceConfig{SocketPath: socket, SpoolRoot: h.spoolRoot, Hello: hello, EnsureCoordinator: func(ctx context.Context) error {
		select {
		case <-rejected:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, InvocationProvider: func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
		calls.Add(1)
		return launchInvocation{args: []string{"/usr/bin/true"}}, nil
	}}, h)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("source terminated before successful revoked replay: %v", err)
	}
	if err = <-serverDone; err != nil {
		t.Fatal(err)
	}
	<-observed
	current, err := os.ReadFile(filepath.Join(original.CapabilityDir, "capability.json"))
	if err != nil || string(current) != string(originalFile) || calls.Load() != 1 {
		t.Fatalf("report file/token/invocation changed: %v calls=%d", err, calls.Load())
	}
	var status, segment, budget, hostStatus string
	var epoch, used, charged, reports, runtimeEvents int64
	err = db.SQL().QueryRow(`SELECT c.status,c.execution_epoch,c.used_bytes,b.used_ms,s.status,b.status,h.status,(SELECT COUNT(*) FROM report_events),(SELECT COUNT(*) FROM runtime_events) FROM report_capabilities c JOIN segment_runtime s ON s.segment_id=c.segment_id JOIN budget_runtime b ON b.id=s.reservation_id JOIN runtime_hosts h ON h.id=s.host_id WHERE c.capability_id=?`, original.CapabilityID).Scan(&status, &epoch, &used, &charged, &segment, &budget, &hostStatus, &reports, &runtimeEvents)
	if err != nil || status != "revoked" || epoch != 2 || used != 0 || charged != 0 || segment != "not_started" || budget != "settled" || hostStatus != "ready" || reports != 0 || runtimeEvents != 1 {
		t.Fatalf("reconnect resurrected/spawned or blocked Host: %s/%d/%d/%s/%s/%s reports=%d events=%d %v", status, epoch, used, segment, budget, hostStatus, reports, runtimeEvents, err)
	}
}
