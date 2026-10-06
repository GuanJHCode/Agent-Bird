package host

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestSourceReplaysPreparedPrelaunchStopRejectionIntoExactStoreSettlement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	binary, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: os.Getpid(), OriginBirth: birth}, Host: store.HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: binary}, Tasks: []store.TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 1, MaxActiveMS: 5000, AdapterPayload: json.RawMessage(`{"provider":"fake","directory":"/private/workspace"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	hello := contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: os.Getpid(), OriginBirth: birth, HostGeneration: "generation", PID: os.Getpid(), Birth: birth, Executable: binary}
	producer, err := db.RegisterHost(ctx, hello, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, producer, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	spoolRoot := filepath.Join(root, "spool")
	h, err := NewIPC(spoolRoot, producer)
	if err != nil {
		t.Fatal(err)
	}
	a := store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID}
	entered := make(chan struct{})
	meta := launchMetadata{commandID: grant.CommandID, workRevision: 1, executionEpoch: 1, prepareProvider: func(prepCtx context.Context) (process.Command, error) {
		close(entered)
		<-prepCtx.Done()
		return process.Command{}, prepCtx.Err()
	}}
	prepared := make(chan error, 1)
	go func() {
		result, err := h.execute(ctx, a, grant.RunID, grant.TaskID, process.Command{Path: "/usr/bin/true"}, false, meta)
		if result.Status != "failed" || !errors.Is(err, context.Canceled) {
			prepared <- errors.New("prelaunch cancellation did not write rejection")
			return
		}
		prepared <- nil
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("preparation not entered")
	}
	stop, err := db.RequestStop(ctx, grant.TaskID, 1, "owner_stop")
	if err != nil {
		t.Fatal(err)
	}
	stopCtx, stopCancel := context.WithDeadline(ctx, time.UnixMilli(stop.Command.DeadlineUnixMS))
	err = h.StopSegment(stopCtx, grant.SegmentID)
	stopCancel()
	if err != nil {
		t.Fatal(err)
	}
	if err = <-prepared; err != nil {
		t.Fatal(err)
	}

	// A fresh RunSource must replay the original same-producer spool before
	// reconciliation. The real framed ACK path commits into the actual Store.
	wireRoot, err := os.MkdirTemp(os.Getenv("TMPDIR"), "c04-wire-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(wireRoot) })
	socket := filepath.Join(wireRoot, "host.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	sourceCtx, cancelSource := context.WithCancel(ctx)
	defer cancelSource()
	go func() {
		serverDone <- func() error {
			conn, err := listener.Accept()
			if err != nil {
				return err
			}
			defer conn.Close()
			deadline, _ := ctx.Deadline()
			_ = conn.SetDeadline(deadline)
			frame, err := ipc.Read(conn)
			if err != nil || frame.Kind != ipc.KindHostHello {
				return errors.New("source hello missing")
			}
			var received contract.HostHello
			if json.Unmarshal(frame.Payload, &received) != nil {
				return errors.New("source hello invalid")
			}
			if host, err := db.RegisterHost(ctx, received, 1); err != nil || host != producer {
				return errors.New("source hello scope rejected")
			}
			payload, _ := json.Marshal(contract.HostReady{HostID: producer, CoordinatorEpoch: 1})
			if err = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindHostReady, RequestID: frame.RequestID, Epoch: 1, Payload: payload}); err != nil {
				return err
			}
			for _, kind := range []string{contract.EventPrepared, contract.EventLaunchRejected} {
				frame, err = ipc.Read(conn)
				if err != nil || frame.Kind != ipc.KindEvent {
					return errors.New("source replay event missing")
				}
				var event contract.Event
				if json.Unmarshal(frame.Payload, &event) != nil || event.Kind != kind || event.ProducerID != producer || event.CommandID != grant.CommandID || event.SegmentID != grant.SegmentID {
					return errors.New("source replay event scope changed")
				}
				ack, err := db.CommitHostEvent(ctx, event)
				if err != nil {
					return err
				}
				if _, err = db.CommitHostEvent(ctx, event); err != nil {
					return err
				}
				payload, _ = json.Marshal(ack)
				if err = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindDurableAck, RequestID: frame.RequestID, Epoch: 1, Payload: payload}); err != nil {
					return err
				}
			}
			frame, err = ipc.Read(conn)
			var state contract.HostReconciled
			if err != nil || frame.Kind != ipc.KindHostReconciled || json.Unmarshal(frame.Payload, &state) != nil || state.PublishedThrough != 2 {
				return errors.New("source durable rejection cursor mismatch")
			}
			// The fixture is complete only after the real durable cursor barrier.
			// EOF is recoverable under the independently living C02 Source.
			cancelSource()
			return nil
		}()
	}()
	err = RunSource(sourceCtx, SourceConfig{SocketPath: socket, SpoolRoot: spoolRoot, ProducerID: producer, Hello: hello, InvocationProvider: func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
		return nil, errors.New("no replacement launch authorized")
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("source replay returned %v", err)
	}
	if err = <-serverDone; err != nil {
		t.Fatal(err)
	}
	var segment, budget, stopped, reason string
	var used, deadline int64
	if err = db.SQL().QueryRow(`SELECT s.status,b.status,b.used_ms,z.status,z.reason,z.deadline_unix_ms FROM segment_runtime s JOIN budget_runtime b ON b.id=s.reservation_id JOIN stop_runtime z ON z.segment_id=s.segment_id AND z.host_id=s.host_id WHERE s.segment_id=?`, grant.SegmentID).Scan(&segment, &budget, &used, &stopped, &reason, &deadline); err != nil || segment != "not_started" || budget != "settled" || used != 0 || stopped != "completed" || reason != "owner_stop" || deadline != stop.Command.DeadlineUnixMS {
		t.Fatalf("source/store settlement=%s/%s/%d/%s/%s/%d %v", segment, budget, used, stopped, reason, deadline, err)
	}
}
