package events

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
)

func BenchmarkPendingSpool(b *testing.B) {
	dir := filepath.Join(b.TempDir(), "spool")
	s, err := Open(dir)
	if err != nil {
		b.Fatal(err)
	}
	for i := int64(1); i <= 1000; i++ {
		if err = s.Append(contract.Event{Version: 1, AttemptID: "a", SegmentID: "s", Sequence: i, Kind: "progress"}); err != nil {
			b.Fatal(err)
		}
	}
	if err = s.AckThrough(990); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		var events []contract.Event
		if incremental, ok := any(s).(interface {
			ReadAfter(int64) ([]contract.Event, error)
		}); ok {
			events, err = incremental.ReadAfter(990)
		} else {
			events, err = s.Read()
			events = events[990:]
		}
		if err != nil || len(events) != 10 {
			b.Fatalf("events=%d err=%v", len(events), err)
		}
	}
}
func TestAckArchivesOnlyDurablePrefixAndPreservesReplay(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 3; i++ {
		if err = s.Append(contract.Event{Version: 1, AttemptID: "a", SegmentID: "s", Sequence: i, Kind: "result"}); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.AckThrough(2); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		if _, err = os.Stat(filepath.Join(dir, "archive", fmt.Sprintf("event-%020d.json", i))); err != nil {
			t.Fatalf("durable event not archived: %v", err)
		}
	}
	incremental, ok := any(s).(interface {
		ReadAfter(int64) ([]contract.Event, error)
	})
	if !ok {
		t.Fatal("missing incremental reader")
	}
	pending, err := incremental.ReadAfter(2)
	if err != nil || len(pending) != 1 || pending[0].Sequence != 3 {
		t.Fatalf("pending=%#v %v", pending, err)
	}
	all, err := s.Read()
	if err != nil || len(all) != 3 {
		t.Fatalf("history lost: %d %v", len(all), err)
	}
	if err = s.Append(all[0]); err != nil {
		t.Fatal(err)
	}
	changed := all[0]
	changed.Kind = "failure"
	if err = s.Append(changed); err == nil {
		t.Fatal("archived sequence overwritten")
	}
}
