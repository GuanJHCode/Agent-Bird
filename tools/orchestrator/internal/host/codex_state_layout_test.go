package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newCodexPrimaryFixture(t *testing.T) (codexStateLayout, codexSlotsFixture) {
	t.Helper()
	f := newCodexSlotsFixture(t)
	state := filepath.Dir(f.home.Path)
	for _, name := range codexAuxiliaryNames {
		if err := os.Mkdir(filepath.Join(state, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(f.home.Path, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.home.Path, "config.toml"), []byte("# owned synthetic config\n"), 0600); err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(auth, []byte("SYNTHETIC_AUTH_CONTENT_NOT_READ"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(auth, filepath.Join(f.home.Path, "auth.json")); err != nil {
		t.Fatal(err)
	}
	layout, err := codexAnchorStateLayout(context.Background(), state)
	if err != nil {
		t.Fatal("closed state layout not available", err)
	}
	return layout, f
}

func TestCodexPrimaryClosedLayoutRecordsOriginalParents(t *testing.T) {
	layout, f := newCodexPrimaryFixture(t)
	got, err := codexCapturePrimaryInventory(context.Background(), layout, &f.binding, codexPrimaryControl{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Entries != 21 || len(got.Parents) == 0 || len(got.Parents) > 32 || len(got.SHA256) != 64 {
		t.Fatalf("closed primary inventory incomplete: entries=%d parents=%d", got.Entries, len(got.Parents))
	}
	seen := map[string]bool{}
	for _, parent := range got.Parents {
		if seen[parent.Path] {
			t.Fatal("duplicate protected parent")
		}
		seen[parent.Path] = true
	}
	if !seen[filepath.Dir(f.binding.RolloutPath)] || !seen[layout.SQLite.Path] || !seen[layout.Auxiliary.Runtime[0].Path] {
		t.Fatal("rollout/SQLite/original runtime anchors omitted")
	}
	if err = os.RemoveAll(filepath.Join(layout.Sessions.Path, "2026")); err != nil {
		t.Fatal(err)
	}
	for _, family := range codexSQLiteFamilies {
		if err = os.Remove(filepath.Join(layout.SQLite.Path, family+".sqlite")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = codexCapturePrimaryInventory(context.Background(), layout, nil, codexPrimaryControl{}); err != nil {
		t.Fatal("fresh initial layout refused", err)
	}
}

func TestCodexPrimaryRejectsAdditionalPersistentInputs(t *testing.T) {
	for _, kind := range []string{"models_cache", "session_index", "unknown_home", "unknown_state", "unknown_sqlite", "foreign_rollout", "compressed_rollout", "temporary_rollout", "foreign_date", "config_symlink", "unsafe_config", "installation_symlink", "relative_auth_link", "foreign_initial_history", "root_alias"} {
		t.Run(kind, func(t *testing.T) {
			layout, f := newCodexPrimaryFixture(t)
			binding := &f.binding
			var path string
			var err error
			switch kind {
			case "models_cache":
				path = filepath.Join(layout.Home.Path, "models_cache.json")
			case "session_index":
				path = filepath.Join(layout.Home.Path, "session_index.jsonl")
			case "unknown_home":
				path = filepath.Join(layout.Home.Path, "foreign")
			case "unknown_state":
				path = filepath.Join(layout.State.Path, "foreign")
			case "unknown_sqlite":
				path = filepath.Join(layout.SQLite.Path, "foreign.sqlite")
			case "foreign_rollout":
				path = filepath.Join(filepath.Dir(f.binding.RolloutPath), "rollout-2026-10-05T12-34-56-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl")
			case "compressed_rollout":
				path = f.binding.RolloutPath + ".zst"
			case "temporary_rollout":
				path = f.binding.RolloutPath + ".decompress.123.0.tmp"
			case "foreign_date":
				err = os.Mkdir(filepath.Join(layout.Sessions.Path, "2025"), 0755)
			case "config_symlink":
				path = filepath.Join(layout.Home.Path, "config.toml")
				err = os.Remove(path)
				if err == nil {
					err = os.Symlink(f.binding.RolloutPath, path)
				}
				path = ""
			case "unsafe_config":
				err = os.Chmod(filepath.Join(layout.Home.Path, "config.toml"), 0644)
			case "installation_symlink":
				path = filepath.Join(layout.Home.Path, "installation_id")
				err = os.Remove(path)
				if err == nil {
					err = os.Symlink(f.binding.RolloutPath, path)
				}
				path = ""
			case "relative_auth_link":
				path = filepath.Join(layout.Home.Path, "auth.json")
				err = os.Remove(path)
				if err == nil {
					err = os.Symlink("auth-relative", path)
				}
				path = ""
			case "foreign_initial_history":
				binding = nil
			case "root_alias":
				layout.Auxiliary.Runtime[4] = layout.Auxiliary.Runtime[3]
			}
			if path != "" {
				err = os.WriteFile(path, nil, 0600)
			}
			if err != nil {
				t.Fatal("pure primary fixture", err)
			}
			if _, err = codexCapturePrimaryInventory(context.Background(), layout, binding, codexPrimaryControl{}); !errors.Is(err, errCodexStateFile) {
				t.Fatalf("%s persistent input accepted: %v", kind, err)
			}
		})
	}
}

func TestCodexPrimaryRechecksNamesAndIdentityBeforeReturn(t *testing.T) {
	for _, kind := range []string{"new_foreign_input", "same_count_sqlite_presence_swap", "date_directory_replaced", "reference_swapped", "canceled_after_scan"} {
		t.Run(kind, func(t *testing.T) {
			layout, f := newCodexPrimaryFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reached := false
			_, err := codexCapturePrimaryInventory(ctx, layout, &f.binding, codexPrimaryControl{BeforeGroupVerify: func() {
				reached = true
				var err error
				switch kind {
				case "new_foreign_input":
					err = os.WriteFile(filepath.Join(layout.Home.Path, "models_cache.json"), nil, 0600)
				case "same_count_sqlite_presence_swap":
					err = os.Remove(filepath.Join(layout.SQLite.Path, "state_5.sqlite"))
					if err == nil {
						err = os.WriteFile(filepath.Join(layout.SQLite.Path, "state_5.sqlite-wal"), nil, 0600)
					}
				case "date_directory_replaced":
					path := filepath.Dir(f.binding.RolloutPath)
					err = os.Rename(path, path+".original")
					if err == nil {
						err = os.Mkdir(path, 0755)
					}
				case "reference_swapped":
					path := filepath.Join(layout.Home.Path, "auth.json")
					err = os.Remove(path)
					if err == nil {
						err = os.Symlink(filepath.Join(t.TempDir(), "another-auth.json"), path)
					}
				case "canceled_after_scan":
					cancel()
				}
				if err != nil {
					t.Fatal("private primary checkpoint", err)
				}
			}})
			if !reached || !errors.Is(err, errCodexStateFile) {
				t.Fatalf("late %s inventory adopted/not reached: %t %v", kind, reached, err)
			}
		})
	}
}
