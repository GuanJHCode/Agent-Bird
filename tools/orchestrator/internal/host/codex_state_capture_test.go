package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexPhysicalCaptureUsesOneSharedActualReadBudget(t *testing.T) {
	layout, f := newCodexPrimaryFixture(t)
	body := []byte("synthetic auxiliary bytes")
	if err := os.WriteFile(filepath.Join(layout.Auxiliary.Runtime[0].Path, "skill"), body, 0644); err != nil {
		t.Fatal(err)
	}
	remaining := codexStateTotalLimit
	state, err := codexCapturePhysicalState(context.Background(), layout, f.binding, f.installation, codexStateReadControl{Remaining: &remaining})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Slots) != 20 || state.Auxiliary.Entries != 1 || state.Auxiliary.Bytes != int64(len(body)) || state.Bytes != 600+int64(len("{\"synthetic\":true}\n"))+36+int64(len(body)) || state.Bytes != codexStateTotalLimit-remaining {
		t.Fatalf("one actual byte budget not retained: %+v", state.Auxiliary)
	}
}

func TestCodexPhysicalCaptureRechecksEarlierInputsAfterLastAuxRead(t *testing.T) {
	for _, kind := range []string{"earlier_main", "absent_wal", "required_absence"} {
		t.Run(kind, func(t *testing.T) {
			layout, f := newCodexPrimaryFixture(t)
			if err := os.WriteFile(filepath.Join(layout.Auxiliary.Runtime[0].Path, "last-aux"), []byte("owned last bytes"), 0644); err != nil {
				t.Fatal(err)
			}
			remaining := codexStateTotalLimit
			reads := 0
			reached := false
			_, err := codexCapturePhysicalState(context.Background(), layout, f.binding, f.installation, codexStateReadControl{Remaining: &remaining, BeforeVerify: func(fd int) {
				reads++
				if reads != 21 {
					return
				}
				if fd < 0 {
					t.Fatal("final auxiliary original FD missing")
				}
				reached = true
				path := filepath.Join(layout.SQLite.Path, "state_5.sqlite")
				body := make([]byte, 100)
				copy(body, "SQLite format 3\x00")
				body[99] = 7
				if kind == "absent_wal" {
					path += "-wal"
					body = nil
				}
				if kind == "required_absence" {
					path = filepath.Join(layout.Home.Path, "models_cache.json")
					body = nil
				}
				if err := os.WriteFile(path, body, 0644); err != nil {
					t.Fatal("real final-aux-read checkpoint", err)
				}
			}})
			if !reached || reads != 21 {
				t.Fatalf("real 20slots+last-aux original reads not reached: %t/%d", reached, reads)
			}
			if !errors.Is(err, errCodexStateFile) {
				t.Fatalf("cross-component %s drift accepted after final auxiliary read: %v", kind, err)
			}
		})
	}
}
