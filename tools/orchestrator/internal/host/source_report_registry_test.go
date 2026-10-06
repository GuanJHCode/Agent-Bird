package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

func TestSourceReportRegistryRemembersBeforeLostACKAndReplaysOriginalBinding(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	h, err := NewIPC(root, "launch")
	if err != nil {
		t.Fatal(err)
	}
	registry := sourceReportRegistry{}
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	old := &sourceSession{conn: client, epoch: 1}
	old.startReader()
	grant := contract.LaunchCommand{ReportCapabilityID: "cap", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1}
	cfg := SourceConfig{SocketPath: "/private/socket"}
	prepared := make(chan error, 1)
	go func() {
		_, err := h.prepareReportCapability(context.Background(), cfg, grant, old, registry.remember)
		prepared <- err
	}()
	frame, err := ipc.Read(peer)
	if err != nil {
		t.Fatal(err)
	}
	var initial contract.ReportCapabilityRegistration
	if err := json.Unmarshal(frame.Payload, &initial); err != nil {
		t.Fatal(err)
	}
	// The preparing worker is still waiting for ACK, so snapshot must already
	// contain the binding. Losing that ACK must not forget it or permit Start.
	snapshot := registry.snapshot()
	if len(snapshot) != 1 || snapshot[0] != reportBinding(initial) {
		t.Fatal("registration snapshot omitted worker before ACK")
	}
	path := filepath.Join(root, ".reports", "cap", "capability.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = peer.Close()
	if err := <-prepared; !errors.Is(err, io.EOF) {
		t.Fatalf("registration ACK-loss error=%v", err)
	}
	newer, newPeer := net.Pipe()
	defer newer.Close()
	defer newPeer.Close()
	next := &sourceSession{conn: newer, epoch: 2}
	next.startReader()
	replayed := make(chan error, 1)
	go func() { replayed <- registry.replay(context.Background(), next) }()
	frame, err = ipc.Read(newPeer)
	if err != nil {
		t.Fatal(err)
	}
	var restored contract.ReportCapabilityRegistration
	if err := json.Unmarshal(frame.Payload, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.ExecutionEpoch != 2 || reportBinding(restored) != reportBinding(initial) {
		t.Fatal("replay replaced original capability binding")
	}
	ack, _ := json.Marshal(contract.DurableAck{EventID: restored.CapabilityID, PayloadHash: restored.TokenHash, Status: "durable"})
	if err := ipc.Write(newPeer, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindDurableAck, RequestID: frame.RequestID, Epoch: 2, Payload: ack}); err != nil {
		t.Fatal(err)
	}
	if err := <-replayed; err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(path)
	if err != nil || string(current) != string(original) {
		t.Fatalf("replay rewrote original worker file: %v", err)
	}
}

func TestSourceReportRegistryRejectsBindingDriftAndBoundsRetention(t *testing.T) {
	registry := sourceReportRegistry{}
	binding := contract.ReportCapabilityRegistration{CapabilityID: "cap", TokenHash: "original", ExecutionEpoch: 1}
	if err := registry.remember(binding); err != nil {
		t.Fatal(err)
	}
	used := registry.bytes
	binding.ExecutionEpoch = 2
	if err := registry.remember(binding); err != nil || registry.bytes != used {
		t.Fatal("same binding consumed extra quota across epoch")
	}
	binding.TokenHash = "foreign"
	if err := registry.remember(binding); err == nil {
		t.Fatal("registry accepted token drift")
	}
	registry.bytes = sourceReportRegistryLimit - 1
	binding.CapabilityID = "new"
	if err := registry.remember(binding); err == nil || len(registry.snapshot()) != 1 {
		t.Fatal("registry exceeded its report-control allowance")
	}
}
