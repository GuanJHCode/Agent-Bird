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

func TestCoreLivingSourceCleanupRegistersOriginalSharedStop(t *testing.T) {
	for _, mode := range []string{"authenticated_release", "owner_cancel", "unproved_tree"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sourceCtx, cancelSource := context.WithCancel(ctx)
			defer cancelSource()
			socket, listener := c02SourceSocket(t)
			hello := c02SourceIdentity(t)
			h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), hello.LaunchID)
			if err != nil {
				t.Fatal(err)
			}
			grant := contract.LaunchCommand{CommandID: "command", ReservationID: "reservation", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, GrantedActiveMS: 5000}
			leaderRelease := filepath.Join(h.spoolRoot, "leader-release")
			childRelease := filepath.Join(h.spoolRoot, "child-release")
			t.Cleanup(func() {
				_ = os.WriteFile(leaderRelease, nil, 0600)
				_ = os.WriteFile(childRelease, nil, 0600)
				if mode != "unproved_tree" {
					return
				}
				// Business UNKNOWN and its original stop window have already
				// been asserted. Keep the owned release file until the original
				// fixture group is absent; TempDir cleanup must not erase it
				// before the child observes release. This adds no worker grace.
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cleanupCancel()
				events, err := h.Collect(cleanupCtx, store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID})
				if err != nil {
					t.Errorf("fixture cleanup cannot read original group: %v", err)
					return
				}
				for _, event := range events {
					if event.Kind != contract.EventSpawned || event.Process == nil {
						continue
					}
					pgid := event.Process.PGID
					ticker := time.NewTicker(10 * time.Millisecond)
					defer ticker.Stop()
					for process.ConfirmGroupExited(pgid) != nil {
						select {
						case <-cleanupCtx.Done():
							t.Errorf("released original fixture group %d remains unproven", pgid)
							return
						case <-ticker.C:
						}
					}
					t.Logf("fixture_cleanup_after_business_unknown pgid=%d group_absent=true", pgid)
					return
				}
			})
			stopAt := make(chan time.Time, 1)
			serverDone := make(chan error, 1)
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
						return errors.New("missing source hello")
					}
					body, _ := json.Marshal(contract.HostReady{HostID: hello.LaunchID, CoordinatorEpoch: 1})
					if err = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindHostReady, RequestID: frame.RequestID, Epoch: 1, Payload: body}); err != nil {
						return err
					}
					frame, err = ipc.Read(conn)
					if err != nil || frame.Kind != ipc.KindHostReconciled {
						return errors.New("missing initial reconcile")
					}
					body, _ = json.Marshal(grant)
					if err = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindLaunch, RequestID: grant.CommandID, Epoch: 1, Payload: body}); err != nil {
						return err
					}
					stopping := false
					for {
						frame, err = ipc.Read(conn)
						if err != nil {
							if errors.Is(err, net.ErrClosed) {
								return nil
							}
							return err
						}
						var event contract.Event
						if frame.Kind != ipc.KindEvent || json.Unmarshal(frame.Payload, &event) != nil {
							return errors.New("invalid source event")
						}
						body, _ = json.Marshal(contract.DurableAck{ProducerID: event.ProducerID, EventID: event.EventID, PayloadHash: event.PayloadHash, AckedThrough: event.Sequence, Status: "durable"})
						if err = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindDurableAck, RequestID: frame.RequestID, Epoch: 1, Payload: body}); err != nil {
							return err
						}
						if event.Kind == contract.EventRunning && !stopping {
							stopping = true
							stopAt <- time.Now()
							if mode == "owner_cancel" {
								cancelSource()
							} else if mode == "authenticated_release" {
								body, _ = json.Marshal(contract.NewHostDisposition(hello, 1, "coordinator_exit"))
								if err = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindHostDisposition, RequestID: "release", Epoch: 1, Payload: body}); err != nil {
									return err
								}
							} else if err = os.WriteFile(leaderRelease, nil, 0600); err != nil {
								return err
							}
						}
						if mode == "unproved_tree" && event.Kind == contract.EventUnknown {
							cancelSource()
							return nil
						}
						if event.Kind == contract.EventExited {
							return nil
						}
					}
				}()
			}()
			err = runLivingSource(sourceCtx, SourceConfig{SocketPath: socket, SpoolRoot: h.spoolRoot, Hello: hello, InvocationProvider: func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
				if mode == "unproved_tree" {
					// The leader stays alive until Running/ACK; its owned child
					// outlives the original real Wait and fails tree absence proof.
					return launchInvocation{args: []string{"/bin/sh", "-c", `(trap '' TERM; while [ ! -f "$2" ]; do /bin/sleep 0.01; done) </dev/null >/dev/null 2>&1 &
while [ ! -f "$1" ]; do /bin/sleep 0.01; done
exit 0`, "core-unproved-tree", leaderRelease, childRelease}}, nil
				}
				// Its own finite OS lifetime prevents an orphan if the RED process
				// panics. Running/ACK is the real launch barrier, never a sleep.
				return launchInvocation{args: []string{"/bin/sh", "-c", "trap 'exit 0' TERM; /bin/sleep 2"}}, nil
			}}, h)
			if mode != "authenticated_release" && !errors.Is(err, context.Canceled) || mode == "authenticated_release" && err != nil {
				t.Fatalf("source cleanup returned %v", err)
			}
			started := <-stopAt
			h.stopMu.Lock()
			intent := h.stopAllIntent
			h.stopMu.Unlock()
			if intent == nil || !intent.bounded || intent.deadline.Before(started) || intent.deadline.After(started.Add(gracefulStopWindow+time.Second)) {
				t.Fatalf("source cleanup did not register the original10s shared scope: %+v", intent)
			}
			if err := <-serverDone; err != nil {
				t.Fatal(err)
			}
			events, err := h.Collect(ctx, store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID})
			if err != nil {
				t.Fatal(err)
			}
			kinds := map[string]int{}
			for _, event := range events {
				kinds[event.Kind]++
			}
			if mode == "unproved_tree" {
				if kinds[contract.EventSpawned] != 1 || kinds[contract.EventUnknown] != 1 || kinds[contract.EventStopped] != 0 || kinds[contract.EventExited] != 0 || kinds[contract.EventLaunchRejected] != 0 || len(h.reservations) != 1 {
					t.Fatalf("Source cleanup replaced UNKNOWN or released original budget: %+v", kinds)
				}
				return
			}
			if kinds[contract.EventSpawned] != 1 || kinds[contract.EventStopped] != 1 || kinds[contract.EventExited] != 1 || kinds[contract.EventUnknown] != 0 {
				t.Fatalf("cleanup duplicated launch/terminal or lost proof: %+v", kinds)
			}
			h.mu.Lock()
			remaining := len(h.activeBySeg) + len(h.segmentCancels)
			h.mu.Unlock()
			if remaining != 0 {
				t.Fatalf("cleanup left original execution registered: %d", remaining)
			}
			var activeMS int64
			for _, event := range events {
				if event.Kind == contract.EventExited {
					activeMS = event.ActiveMS
				}
			}
			if activeMS <= 0 || time.Since(started) > gracefulStopWindow+time.Second {
				t.Fatalf("cleanup budget/confirmation=%d/%v", activeMS, time.Since(started))
			}
		})
	}
}
