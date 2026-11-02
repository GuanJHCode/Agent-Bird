package events

import (
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRejectsExistingPublicDirectoryWithoutChangingMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("open err=%v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("mode=%v", info.Mode().Perm())
	}
}

func TestSpoolPersistsEventsAndDurableAck(t *testing.T) {
	s, err := Open(privateTempDir(t))
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
	s, err := Open(privateTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMetadata("launch.json", map[string]any{"phase": "prepared", "version": 1}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := s.ReadMetadata("launch.json", &got); err != nil {
		t.Fatal(err)
	}
	if got["phase"] != "prepared" {
		t.Fatalf("metadata=%#v", got)
	}
	if err := s.WriteMetadata("launch.json", map[string]any{"phase": "running", "version": 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReadMetadata("launch.json", &got); err != nil {
		t.Fatal(err)
	}
	if got["phase"] != "running" {
		t.Fatalf("metadata=%#v", got)
	}
}

func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}
