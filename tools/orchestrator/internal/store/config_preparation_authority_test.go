package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
)

func TestStoreConfigAuthorityNodesAndGrants(t *testing.T) {
	for _, scenario := range []string{"same-owner-new-grant", "normal-reopen", "foreign-owner", "missing-grant", "replaced-database", "replaced-state-root", "unsafe-root", "unsafe-database"} {
		t.Run(scenario, func(t *testing.T) {
			root, e := filepath.EvalSymlinks(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			if e = os.Chmod(root, 0700); e != nil {
				t.Fatal(e)
			}
			db, e := Open(filepath.Join(root, "state.db"))
			if e != nil {
				t.Fatal(e)
			}
			defer func() { db.Close() }()
			ctx := context.Background()
			first, e := db.CreateOwnerGrant(ctx, "controller", 111, "birth")
			if e != nil {
				t.Fatal(e)
			}
			a, e := db.ConfigPreparationAuthority(ctx, first, nil)
			if e != nil {
				t.Fatal(e)
			}
			current := first
			switch scenario {
			case "same-owner-new-grant":
				current, e = db.CreateOwnerGrant(ctx, "controller", 111, "birth")
				if e != nil {
					t.Fatal(e)
				}
				if current.ID == first.ID {
					t.Fatal("grant not fresh")
				}
			case "normal-reopen":
				if e = db.Close(); e != nil {
					t.Fatal(e)
				}
				db, e = Open(filepath.Join(root, "state.db"))
				if e != nil {
					t.Fatal(e)
				}
			case "foreign-owner":
				current, e = db.CreateOwnerGrant(ctx, "foreign", 112, "other")
				if e != nil {
					t.Fatal(e)
				}
			case "missing-grant":
				if _, e = db.sql.Exec(`DELETE FROM owner_grants WHERE id=?`, first.ID); e != nil {
					t.Fatal(e)
				}
			case "replaced-database":
				if e = os.Rename(filepath.Join(root, "state.db"), filepath.Join(root, "old.db")); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(root, "state.db"), []byte("foreign"), 0600); e != nil {
					t.Fatal(e)
				}
			case "replaced-state-root":
				backup := root + "-original"
				if e = os.Rename(root, backup); e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() { os.RemoveAll(backup) })
				if e = os.Mkdir(root, 0700); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(root, "state.db"), []byte("foreign"), 0600); e != nil {
					t.Fatal(e)
				}
			case "unsafe-root":
				if e = os.Chmod(root, 0755); e != nil {
					t.Fatal(e)
				}
			case "unsafe-database":
				if e = os.Chmod(filepath.Join(root, "state.db"), 0644); e != nil {
					t.Fatal(e)
				}
			}
			got, e := db.ConfigPreparationAuthority(ctx, current, &a)
			if scenario == "same-owner-new-grant" || scenario == "normal-reopen" {
				if e != nil || got != a {
					t.Fatal("original authority changed", got, e)
				}
			} else if !errors.Is(e, install.ErrConfigPreparationStoreMismatch) && (e == nil || e.Error() != "owner_capability_mismatch") {
				t.Fatal("foreign/replaced/unsafe/missing authority accepted", got, e)
			}
		})
	}
}
