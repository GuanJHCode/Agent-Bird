package events

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestHistoryReadAcrossIndependentPublisherArchive(t *testing.T) {
	for _, scenario := range []string{"archive", "missing", "invalid_json", "wrong_sequence", "oversize", "symlink", "pending_suffix"} {
		t.Run(scenario, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "spool")
			writer, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			first := contract.Event{Version: 1, AttemptID: "attempt", SegmentID: "segment", Sequence: 1, Kind: "result"}
			second := first
			second.Sequence = 2
			if err = writer.Append(first); err != nil {
				t.Fatal(err)
			}
			if err = writer.Append(second); err != nil {
				t.Fatal(err)
			}
			reader, err := Open(dir) // An independent instance has its own mutex.
			if err != nil {
				t.Fatal(err)
			}
			active := filepath.Join(dir, "event-00000000000000000001.json")
			archived := filepath.Join(dir, "archive", filepath.Base(active))
			moved := false
			history, err := reader.readEventsWithOpener(0, scenario != "pending_suffix", func(path string) (*os.File, error) {
				if path == active && !moved {
					moved = true
					// Both directory snapshots already exist. Migrate the real
					// file before the reader opens its now-stale active path.
					if err := writer.AckThrough(1); err != nil {
						t.Fatal(err)
					}
					switch scenario {
					case "missing", "symlink":
						if err := os.Remove(archived); err != nil {
							t.Fatal(err)
						}
						if scenario == "symlink" {
							if err := os.Symlink(filepath.Join(dir, "event-00000000000000000002.json"), archived); err != nil {
								t.Fatal(err)
							}
						}
					case "invalid_json", "wrong_sequence", "oversize":
						body := []byte(`not json`)
						if scenario == "wrong_sequence" {
							body = []byte(`{"version":1,"sequence":9}`)
						} else if scenario == "oversize" {
							body = []byte(strings.Repeat(" ", 64*1024+1))
						}
						if err := os.WriteFile(archived, body, 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
				return openEventFile(path)
			})
			if !moved {
				t.Fatal("archive race was not exercised")
			}
			switch scenario {
			case "archive":
				if err != nil || len(history) != 2 || history[0].Sequence != 1 || history[1].Sequence != 2 || history[0].AttemptID != first.AttemptID || history[0].Kind != first.Kind {
					t.Fatalf("concurrent archive lost history: %+v %v", history, err)
				}
			case "missing", "pending_suffix":
				var pathErr *os.PathError
				if !errors.As(err, &pathErr) || !errors.Is(err, os.ErrNotExist) || pathErr.Path != active || history != nil {
					t.Fatal("missing active event became partial success or suffix history", history, err)
				}
			case "invalid_json", "wrong_sequence":
				if err == nil || err.Error() != "invalid_event" || history != nil {
					t.Fatal("archive fallback bypassed event validation", history, err)
				}
			case "oversize":
				if err == nil || err.Error() != "event_too_large" || history != nil {
					t.Fatal("archive fallback bypassed size bound", history, err)
				}
			case "symlink":
				if err == nil || errors.Is(err, os.ErrNotExist) || history != nil {
					t.Fatal("archive fallback followed a link or failed to inspect it", history, err)
				}
			}
		})
	}
}

func TestHistoryReadStillRejectsActiveArchiveConflict(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Append(contract.Event{Version: 1, AttemptID: "attempt", SegmentID: "segment", Sequence: 1, Kind: "result"}); err != nil {
		t.Fatal(err)
	}
	if err = s.AckThrough(1); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.dir, "event-00000000000000000001.json"), []byte(`{"version":1,"sequence":1,"kind":"failure"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if history, err := s.Read(); !errors.Is(err, ErrConflict) || history != nil {
		t.Fatal("active/archive collision was hidden", history, err)
	}
}
