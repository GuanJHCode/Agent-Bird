package host

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

func TestCoreSourceScopeTransitionNeedsExactAckAndPreservesEveryCounter(t *testing.T) {
	for _, name := range []string{"verified_pending", "verified_unavailable", "lost_ack", "request_id", "epoch", "launch", "prior", "scope", "status", "kind", "disk_changed", "canceled_after_ack"} {
		t.Run(name, func(t *testing.T) {
			old, next := strings.Repeat("a", 64), strings.Repeat("b", 64)
			s, err := openHostRecovery(privateRecoveryTestDirectory(t), old, 101, "original-host")
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			s.record.Attempts, s.record.Pending, s.record.NextAttemptUnixMS = 3, true, time.Now().Add(4*time.Second).UnixMilli()
			if name == "verified_unavailable" {
				s.record.Attempts, s.record.Pending, s.record.Unavailable, s.record.NextAttemptUnixMS = 5, false, true, time.Now().Add(-time.Second).UnixMilli()
			}
			if err := s.persist(); err != nil {
				t.Fatal(err)
			}
			before, beforeBytes := s.record, append([]byte(nil), s.bytes...)
			hello := contract.HostHello{LaunchID: "original-launch", OriginContextID: "original-owner", HostGeneration: "new-generation", PID: 909, Birth: "new-host"}
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			done := make(chan error, 1)
			go func() {
				defer server.Close()
				request, err := ipc.Read(server)
				if err != nil {
					done <- err
					return
				}
				var body contract.HostRecoveryScopeRequest
				if err := json.Unmarshal(request.Payload, &body); err != nil {
					done <- err
					return
				}
				if request.Kind != ipc.KindHostRecoveryScope || body.PriorScopeSHA256 != old || body.Hello != hello {
					done <- net.ErrClosed
					return
				}
				if name == "lost_ack" {
					done <- nil
					return
				}
				ack := contract.HostRecoveryScopeAck{Status: "verified_scope_transition", LaunchID: hello.LaunchID, PriorScopeSHA256: old, ScopeSHA256: next}
				response := ipc.Envelope{Version: ipc.Version, Kind: ipc.KindResponse, RequestID: request.RequestID, Epoch: 3}
				switch name {
				case "request_id":
					response.RequestID = "unrelated"
				case "epoch":
					response.Epoch = 0
				case "launch":
					ack.LaunchID = "foreign"
				case "prior":
					ack.PriorScopeSHA256 = "foreign"
				case "scope":
					ack.ScopeSHA256 = "foreign"
				case "status":
					ack.Status = "unverified"
				case "kind":
					response.Kind = ipc.KindError
				}
				response.Payload, _ = json.Marshal(ack)
				done <- ipc.Write(server, response)
			}()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err = transitionSourceRecovery(ctx, s, hello, next, func(ctx context.Context, request contract.HostRecoveryScopeRequest, expected string) error {
				// The flock must remain exclusive during the remote proof.
				if opened, err := openHostRecovery(s.directory, old, 909, "new-host"); err == nil {
					opened.close()
					t.Error("transition released original flock")
				}
				if err := exchangeRecoveryScope(ctx, client, request, expected); err != nil {
					return err
				}
				if name == "disk_changed" {
					return os.WriteFile(s.path, []byte("external change\n"), 0600)
				}
				if name == "canceled_after_ack" {
					cancel()
				}
				return nil
			})
			if serverErr := <-done; serverErr != nil {
				t.Fatal(serverErr)
			}
			allowed := strings.HasPrefix(name, "verified_")
			if (err == nil) != allowed {
				t.Fatalf("transition err=%v", err)
			}
			wanted := before
			if allowed {
				wanted.ScopeSHA256 = next
			}
			if !reflect.DeepEqual(s.record, wanted) {
				t.Fatalf("counter/identity changed: before=%+v after=%+v", before, s.record)
			}
			disk, readErr := os.ReadFile(s.path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !allowed && name != "disk_changed" && !bytes.Equal(disk, beforeBytes) {
				t.Fatal("unacknowledged proof changed original disk bytes")
			}
			if name == "disk_changed" && string(disk) != "external change\n" {
				t.Fatal("transition overwrote external change")
			}
			if allowed {
				s.close()
				reopened, err := openHostRecovery(s.directory, next, 999, "third-host")
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.close()
				if reopened.record != wanted {
					t.Fatal("cold reopen reset retained reservation")
				}
			}
		})
	}
}

func TestCoreRecoveryScopeHashKeepsExactV1Encoding(t *testing.T) {
	hello := contract.HostHello{LaunchID: "launch", OriginContextID: "origin", HostGeneration: "generation", Executable: "/pinned"}
	pin := strings.Repeat("a", 64)
	raw, _ := json.Marshal(struct{ LaunchID, OriginContextID, HostGeneration, Executable, ExecutableSHA256 string }{hello.LaunchID, hello.OriginContextID, hello.HostGeneration, hello.Executable, pin})
	if contract.HostRecoveryScopeSHA256(hello, pin) != hashBytes(raw) {
		t.Fatal("existing v1 scope changed")
	}
}
