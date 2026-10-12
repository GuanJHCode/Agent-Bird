package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
)

type codexSlotsFixture struct {
	home, sqlite codexStateRoot
	binding      codexsession.ThreadBinding
	installation string
}

func newCodexSlotsFixture(t *testing.T) codexSlotsFixture {
	t.Helper()
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	home, sqlite := filepath.Join(state, "home"), filepath.Join(state, "sqlite")
	for _, path := range []string{home, sqlite} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	f := codexSlotsFixture{installation: "11111111-2222-3333-4444-555555555555", binding: codexsession.ThreadBinding{ThreadID: "12345678-1234-1234-1234-123456789abc", Model: "synthetic-model", ModelProvider: "openai", HistoryMode: "paginated"}}
	f.binding.RolloutPath = filepath.Join(home, "sessions", "2026", "10", "05", "rollout-2026-10-05T12-34-56-"+f.binding.ThreadID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(f.binding.RolloutPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.binding.RolloutPath, []byte("{\"synthetic\":true}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "installation_id"), []byte(f.installation), 0644); err != nil {
		t.Fatal(err)
	}
	for _, family := range codexSQLiteFamilies {
		body := make([]byte, 100)
		copy(body, "SQLite format 3\x00")
		if err := os.WriteFile(filepath.Join(sqlite, family+".sqlite"), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	f.home, err = codexAnchorStateRoot(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	f.sqlite, err = codexAnchorStateRoot(context.Background(), sqlite)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f codexSlotsFixture) capture(t *testing.T) []codexPersistentSlot {
	t.Helper()
	remaining := codexStateTotalLimit
	got, err := codexCapturePersistentSlots(context.Background(), f.home, f.sqlite, f.binding, f.installation, codexStateReadControl{Remaining: &remaining})
	if err != nil {
		t.Fatal("fixed paginated slots unavailable", err)
	}
	return got
}

func TestCodexPaginatedFixedTwentySlotsPreserveOpaquePresence(t *testing.T) {
	f := newCodexSlotsFixture(t)
	if err := os.WriteFile(filepath.Join(f.sqlite.Path, "state_5.sqlite-wal"), []byte("opaque WAL bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.sqlite.Path, "goals_1.sqlite-shm"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	got := f.capture(t)
	if len(got) != 20 {
		t.Fatalf("fixed slots=%d", len(got))
	}
	for i, family := range codexSQLiteFamilies {
		if got[i*3].Name != "sqlite/"+family+".sqlite" || !got[i*3].File.Present || got[i*3].File.Identity.Size != 100 {
			t.Fatal("main identity/header slot lost")
		}
		for j, suffix := range []string{"-wal", "-shm"} {
			if got[i*3+j+1].Name != "sqlite/"+family+".sqlite"+suffix {
				t.Fatal("sidecar identity slot lost")
			}
		}
	}
	if !got[1].File.Present || got[2].File.Present || !got[8].File.Present || got[8].File.Identity.Size != 0 || got[18].Name != "home/sessions/2026/10/05/"+filepath.Base(f.binding.RolloutPath) || got[19].Name != "home/installation_id" {
		t.Fatal("explicit presence/rollout/installation slots incomplete")
	}
	remaining := codexStateTotalLimit
	if err := codexValidatePersistentSlots(context.Background(), f.home, f.sqlite, f.binding, f.installation, got, codexStateReadControl{Remaining: &remaining}); err != nil {
		t.Fatal(err)
	}
}

func TestCodexPaginatedRequiredOutputAndActualLimits(t *testing.T) {
	for _, kind := range []string{"missing_main", "short_main", "bad_header", "missing_rollout", "unfinished_rollout", "missing_installation", "changed_installation", "sidecar_hardlink", "sidecar_symlink", "wrong_home", "wrong_history", "actual_total_limit"} {
		t.Run(kind, func(t *testing.T) {
			f := newCodexSlotsFixture(t)
			main := filepath.Join(f.sqlite.Path, "thread_history_1.sqlite")
			remaining := codexStateTotalLimit
			var err error
			switch kind {
			case "missing_main":
				err = os.Remove(main)
			case "short_main":
				err = os.WriteFile(main, []byte("SQLite format 3\x00"), 0644)
			case "bad_header":
				err = os.WriteFile(main, make([]byte, 100), 0644)
			case "missing_rollout":
				err = os.Remove(f.binding.RolloutPath)
			case "unfinished_rollout":
				err = os.WriteFile(f.binding.RolloutPath, []byte("not newline terminated"), 0644)
			case "missing_installation":
				err = os.Remove(filepath.Join(f.home.Path, "installation_id"))
			case "changed_installation":
				err = os.WriteFile(filepath.Join(f.home.Path, "installation_id"), []byte("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"), 0644)
			case "sidecar_hardlink":
				err = os.Link(main, filepath.Join(f.sqlite.Path, "state_5.sqlite-wal"))
			case "sidecar_symlink":
				err = os.Symlink(main, filepath.Join(f.sqlite.Path, "state_5.sqlite-wal"))
			case "wrong_home":
				f.home.Path = filepath.Dir(f.home.Path)
			case "wrong_history":
				f.binding.HistoryMode = "legacy"
			case "actual_total_limit":
				remaining = 605
			}
			if err != nil {
				t.Fatal("pure slot fixture", err)
			}
			if _, err = codexCapturePersistentSlots(context.Background(), f.home, f.sqlite, f.binding, f.installation, codexStateReadControl{Remaining: &remaining}); !errors.Is(err, errCodexStateFile) {
				t.Fatalf("%s output accepted: %v", kind, err)
			}
			if remaining < 0 || kind == "actual_total_limit" && remaining == 605 {
				t.Fatal("actual cumulative bytes not charged or cap exceeded")
			}
		})
	}
}

func TestCodexPaginatedPriorSlotsRejectOfflineDrift(t *testing.T) {
	for _, kind := range []string{"main_bytes", "same_bytes_new_inode", "sidecar_appeared", "sidecar_disappeared", "slot_omitted", "slot_renamed", "installation_newline"} {
		t.Run(kind, func(t *testing.T) {
			f := newCodexSlotsFixture(t)
			wal := filepath.Join(f.sqlite.Path, "state_5.sqlite-wal")
			if kind == "sidecar_disappeared" {
				if err := os.WriteFile(wal, []byte("opaque"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			original := f.capture(t)
			before := append([]codexPersistentSlot(nil), original...)
			path := filepath.Join(f.sqlite.Path, "state_5.sqlite")
			var err error
			switch kind {
			case "main_bytes":
				body := make([]byte, 100)
				copy(body, "SQLite format 3\x00")
				body[99] = 1
				err = os.WriteFile(path, body, 0644)
			case "same_bytes_new_inode":
				body, e := os.ReadFile(path)
				err = e
				if err == nil {
					err = os.WriteFile(path+".new", body, 0644)
				}
				if err == nil {
					err = os.Rename(path+".new", path)
				}
			case "sidecar_appeared":
				err = os.WriteFile(wal, nil, 0600)
			case "sidecar_disappeared":
				err = os.Remove(wal)
			case "slot_omitted":
				original = original[:19]
			case "slot_renamed":
				original[0].Name = "sqlite/foreign.sqlite"
			case "installation_newline":
				err = os.WriteFile(filepath.Join(f.home.Path, "installation_id"), []byte(f.installation+"\n"), 0644)
			}
			if err != nil {
				t.Fatal("pure offline drift", err)
			}
			remaining := codexStateTotalLimit
			if err = codexValidatePersistentSlots(context.Background(), f.home, f.sqlite, f.binding, f.installation, original, codexStateReadControl{Remaining: &remaining}); !errors.Is(err, errCodexStateFile) {
				t.Fatalf("offline %s resealed/adopted: %v", kind, err)
			}
			if kind != "slot_omitted" && kind != "slot_renamed" && !reflect.DeepEqual(before, original) {
				t.Fatal("prior protected input was rewritten")
			}
		})
	}
}

func TestCodexPaginatedGroupRechecksEarlierSlotBeforeReturn(t *testing.T) {
	for _, kind := range []string{"earlier_file_changed", "absent_sidecar_inserted"} {
		t.Run(kind, func(t *testing.T) {
			f := newCodexSlotsFixture(t)
			remaining := codexStateTotalLimit
			reads := 0
			_, err := codexCapturePersistentSlots(context.Background(), f.home, f.sqlite, f.binding, f.installation, codexStateReadControl{Remaining: &remaining, BeforeVerify: func(int) {
				reads++
				if reads != 20 {
					return
				}
				path := filepath.Join(f.sqlite.Path, "state_5.sqlite")
				body := make([]byte, 100)
				copy(body, "SQLite format 3\x00")
				body[99] = 1
				if kind == "absent_sidecar_inserted" {
					path += "-wal"
					body = nil
				}
				if err := os.WriteFile(path, body, 0644); err != nil {
					t.Fatal("private final-read checkpoint", err)
				}
			}})
			if reads != 20 {
				t.Fatalf("original final read not reached: %d", reads)
			}
			if !errors.Is(err, errCodexStateFile) {
				t.Fatalf("earlier group slot drift accepted: %v", err)
			}
		})
	}
}
