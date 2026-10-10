package gitops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFilterWorktreeBindingKeepsOriginalSource(t *testing.T) {
	for _, mode := range []string{"lifetime_and_routing", "foreign_and_attribute_drift"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			source, _ := repo(t)
			m := configureFilter(t, source, filterManifestFor(t, filterGitPath(t), filterTool(t, "clean"), filterTool(t, "smudge")))
			git(t, source, "add", ".gitattributes")
			git(t, source, "commit", "-m", "attributes")
			path := filepath.Join(t.TempDir(), "worktree")
			git(t, source, "worktree", "add", "--detach", path, "HEAD")
			g, err := OpenFilterGit(ctx, source, m)
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			sourceDigest := g.SHA256()
			bound, err := g.BindWorktree(ctx, path)
			if err != nil || bound == nil {
				t.Fatal("valid fresh binding", err)
			}
			defer bound.Close()
			if bound.SourceSHA256() != sourceDigest || g.SHA256() != sourceDigest {
				t.Fatal("binding changed cross-Host source digest")
			}
			if _, err = run(bound.Context(ctx), path, "rev-parse", "HEAD"); err != nil {
				t.Fatal("bound routing", err)
			}
			if _, err = run(g.Context(ctx), path, "rev-parse", "HEAD"); err == nil {
				t.Fatal("source guard widened to arbitrary worktrees")
			}
			if _, err = run(bound.Context(ctx), source, "rev-parse", "HEAD"); err == nil {
				t.Fatal("worktree context became source executor")
			}
			if mode == "lifetime_and_routing" {
				if err = bound.Close(); err != nil {
					t.Fatal(err)
				}
				if err = g.Verify(ctx); err != nil {
					t.Fatal("child close invalidated source", err)
				}
				if _, err = run(bound.Context(ctx), path, "rev-parse", "HEAD"); err == nil {
					t.Fatal("closed child remained usable")
				}
				bound, err = g.BindWorktree(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				defer bound.Close()
				g.Close()
				if _, err = run(bound.Context(ctx), path, "rev-parse", "HEAD"); err == nil {
					t.Fatal("child outlived original source")
				}
			} else {
				foreign, _ := repo(t)
				configureFilter(t, foreign, m)
				if other, e := g.BindWorktree(ctx, foreign); e == nil {
					if other != nil {
						other.Close()
					}
					t.Fatal("foreign common directory bound")
				}
				borrowed := filepath.Join(filepath.Dir(path), "borrowed")
				if err = os.Mkdir(borrowed, 0700); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{".git", ".gitattributes", "a.txt"} {
					body, e := os.ReadFile(filepath.Join(path, name))
					if e != nil {
						t.Fatal(e)
					}
					if e = os.WriteFile(filepath.Join(borrowed, name), body, 0600); e != nil {
						t.Fatal(e)
					}
				}
				if other, e := g.BindWorktree(ctx, borrowed); e == nil {
					if other != nil {
						other.Close()
					}
					t.Fatal("another worktree index was adopted")
				}
				if err = os.WriteFile(filepath.Join(path, ".gitattributes"), []byte("a.txt filter=foreign\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err = bound.Verify(ctx); err == nil {
					t.Fatal("worktree attribute drift accepted")
				}
				if err = g.Verify(ctx); err != nil {
					t.Fatal("child attribute drift changed source", err)
				}
				bound.Close()
				original, e := os.ReadFile(filepath.Join(source, ".gitattributes"))
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(path, ".gitattributes"), original, 0600); e != nil {
					t.Fatal(e)
				}
				bound, err = g.BindWorktree(ctx, path)
				if err != nil {
					t.Fatal("fresh child before parent drift", err)
				}
				defer bound.Close()
				if err = bound.Verify(ctx); err != nil {
					t.Fatal("baseline before parent drift", err)
				}
				if err = os.WriteFile(filepath.Join(source, ".gitattributes"), []byte("a.txt filter=foreign\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err = bound.Verify(ctx); err == nil {
					t.Fatal("source drift accepted")
				}
			}
		})
	}
}
