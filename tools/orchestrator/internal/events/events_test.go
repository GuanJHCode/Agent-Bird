package events

import (
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"testing"
)

func TestSpoolPersistsEventsAndDurableAck(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := contract.Event{Version: 1, RunID: "r", TaskID: "t", AttemptID: "a", SegmentID: "s", Sequence: 1, Kind: "result", PayloadHash: "abc"}
	if err := s.Append(e); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read()
	if err != nil || len(got) != 1 || got[0].PayloadHash != "abc" {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	if err := s.AckThrough(1); err != nil {
		t.Fatal(err)
	}
	if ack, err := s.AckedThrough(); err != nil || ack != 1 {
		t.Fatalf("ack=%d err=%v", ack, err)
	}
}

func TestMetadataPublicationIsBoundedAndReplaceable(t *testing.T) {
	s, err := Open(t.TempDir()); if err != nil { t.Fatal(err) }
	if err := s.WriteMetadata("launch.json", map[string]any{"phase":"prepared","version":1}); err != nil { t.Fatal(err) }
	var got map[string]any; if err := s.ReadMetadata("launch.json", &got); err != nil { t.Fatal(err) }
	if got["phase"] != "prepared" { t.Fatalf("metadata=%#v", got) }
	if err := s.WriteMetadata("launch.json", map[string]any{"phase":"running","version":1}); err != nil { t.Fatal(err) }
	if err := s.ReadMetadata("launch.json", &got); err != nil { t.Fatal(err) }
	if got["phase"] != "running" { t.Fatalf("metadata=%#v", got) }
}
