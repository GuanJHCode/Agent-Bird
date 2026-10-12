package gitops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFilterSourceRepositoryLinksCannotRedirectGit(t *testing.T) {
	for _, mode := range []string{"gitdir", "commondir"} {
		t.Run(mode, func(t *testing.T) {
			source, _ := repo(t)
			m := configureFilter(t, source, filterManifestFor(t, filterGitPath(t), filterTool(t, "clean"), filterTool(t, "smudge")))
			git(t, source, "add", ".gitattributes")
			git(t, source, "commit", "-m", "attributes")
			parent := t.TempDir()
			worktree, other := filepath.Join(parent, "one"), filepath.Join(parent, "two")
			git(t, source, "worktree", "add", "--detach", worktree, "HEAD")
			git(t, source, "worktree", "add", "--detach", other, "HEAD")
			g, err := OpenFilterGit(context.Background(), worktree, m)
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			if err = g.Verify(context.Background()); err != nil {
				t.Fatal("unchanged linked source", err)
			}
			if mode == "gitdir" {
				replacement, err := os.ReadFile(filepath.Join(other, ".git"))
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(worktree, ".git"), replacement, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				foreign, _ := repo(t)
				configureFilter(t, foreign, m)
				gitdir := git(t, worktree, "rev-parse", "--absolute-git-dir")
				if err = os.WriteFile(filepath.Join(gitdir, "commondir"), []byte(filepath.Join(foreign, ".git")+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err = g.Verify(context.Background()); err == nil {
				t.Fatal("source Git metadata redirect was adopted")
			}
			if _, err = run(g.Context(context.Background()), worktree, "rev-parse", "--absolute-git-dir"); err == nil {
				t.Fatal("redirected source context remained usable")
			}
		})
	}
}
