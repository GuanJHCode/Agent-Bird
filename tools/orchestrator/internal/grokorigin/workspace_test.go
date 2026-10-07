//go:build !windows

package grokorigin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func originWorkspace(t *testing.T) (string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	work := filepath.Join(repo, "nested", "work")
	if err = os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	return repo, work
}
func TestOriginWorkspaceRejectsAncestorCapabilities(t *testing.T) {
	for _, source := range []string{".grok/config.toml", ".mcp.json", ".claude/settings.json", ".grok/hooks", ".grok/plugins", ".claude-plugin", ".grok/skills", ".claude/agents", ".grok/lsp.json"} {
		t.Run(source, func(t *testing.T) {
			repo, work := originWorkspace(t)
			path := filepath.Join(repo, source)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := inspectWorkspace(work); err == nil || err.Error() != "grokorigin_capability_source" {
				t.Fatalf("source accepted: %v", err)
			}
		})
	}
}
func TestOriginWorkspaceAllowsInstructionsButSealsCompleteAncestorChain(t *testing.T) {
	repo, work := originWorkspace(t)
	above := filepath.Dir(repo)
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("ordinary instruction text"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(above, ".grok"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(above, ".grok", "config.toml"), []byte("not in this repository"), 0600); err != nil {
		t.Fatal(err)
	}
	denied, err := inspectWorkspace(work)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{filepath.Join(work, ".mcp.json"), filepath.Join(repo, ".grok", "config.toml"), filepath.Join(above, ".grok", "config.toml")} {
		found := false
		for _, p := range denied {
			if p == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("late-created source not sealed: %s", want)
		}
	}
}
func TestOriginWorkspaceRejectsSymlinkAndUnsafeBoundary(t *testing.T) {
	for _, kind := range []string{"workspace_link", "source_link", "source_parent_link", "git_link", "writable", "relative", "deep"} {
		t.Run(kind, func(t *testing.T) {
			repo, work := originWorkspace(t)
			switch kind {
			case "workspace_link":
				link := filepath.Join(repo, "link")
				if err := os.Symlink(work, link); err != nil {
					t.Fatal(err)
				}
				work = link
			case "source_link":
				if err := os.Symlink("missing", filepath.Join(work, ".mcp.json")); err != nil {
					t.Fatal(err)
				}
			case "source_parent_link":
				outside := t.TempDir()
				if err := os.Symlink(outside, filepath.Join(work, ".grok")); err != nil {
					t.Fatal(err)
				}
			case "git_link":
				if err := os.Remove(filepath.Join(repo, ".git")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("missing", filepath.Join(repo, ".git")); err != nil {
					t.Fatal(err)
				}
			case "writable":
				if err := os.Chmod(work, 0777); err != nil {
					t.Fatal(err)
				}
			case "relative":
				work = "relative"
			case "deep":
				work = filepath.Join(work, strings.Repeat("d/", 65))
				work = filepath.Clean(work)
				if err := os.MkdirAll(work, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := inspectWorkspace(work); err == nil {
				t.Fatal("unsafe workspace accepted")
			}
		})
	}
}
