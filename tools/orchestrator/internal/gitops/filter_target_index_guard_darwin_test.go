//go:build darwin

package gitops

import (
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestTargetIndexGuardRejectsNamedDirectoryReplacement(t *testing.T) {
	for _, mode := range []string{"gitdir", "info"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			gitdir := filepath.Join(root, "gitdir")
			info := filepath.Join(gitdir, "info")
			if err := os.MkdirAll(info, 0700); err != nil {
				t.Fatal(err)
			}
			raw := make([]byte, 32)
			copy(raw, "DIRC")
			binary.BigEndian.PutUint32(raw[4:8], 2)
			if err := os.WriteFile(filepath.Join(gitdir, "index"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			id, err := directoryIdentity(gitdir)
			if err != nil {
				t.Fatal(err)
			}
			source := &FilterGitContext{}
			source.repository.gitDir, source.repository.gitIdentity = gitdir, id
			guard, err := openFilterTargetIndexGuard(source)
			if err != nil {
				t.Fatal(err)
			}
			defer guard.Close()
			if err = guard.Verify(); err != nil {
				t.Fatal("healthy guard", err)
			}
			guard.afterMetadataRead = func() {
				path := gitdir
				if mode == "info" {
					path = info
				}
				if err := os.Rename(path, path+"-held"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				if mode == "info" {
					if err := os.WriteFile(filepath.Join(path, "sparse-checkout"), []byte("/*"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err = guard.Verify(); err == nil {
				t.Fatal("replacement accepted after held metadata read")
			}
		})
	}
}

type filterTargetGuardJournal struct {
	afterIntent func()
}

func (j *filterTargetGuardJournal) RecordGitProcess(_ context.Context, r GitProcessRecord) error {
	if r.Phase == "intent" && j.afterIntent != nil {
		j.afterIntent()
	}
	return nil
}

func TestOpenFilterTargetGitStopsBeforeStartAfterIntentIndexDrift(t *testing.T) {
	for _, runner := range []struct {
		name  string
		child bool
		run   func(context.Context, string, func(context.Context, process.Command) (*process.Handle, error)) error
	}{
		{"bytes", false, func(ctx context.Context, repo string, start func(context.Context, process.Command) (*process.Handle, error)) error {
			_, err := candidateGitWithStart(ctx, repo, nil, start, "rev-parse", "HEAD")
			return err
		}},
		{"stream", false, func(ctx context.Context, repo string, start func(context.Context, process.Command) (*process.Handle, error)) error {
			return candidateGitInputStreamWithStart(ctx, repo, nil, io.Discard, start, "rev-parse", "HEAD")
		}},
		{"child_bytes", true, func(ctx context.Context, repo string, start func(context.Context, process.Command) (*process.Handle, error)) error {
			_, err := candidateGitWithStart(ctx, repo, nil, start, "rev-parse", "HEAD")
			return err
		}},
		{"child_stream", true, func(ctx context.Context, repo string, start func(context.Context, process.Command) (*process.Handle, error)) error {
			return candidateGitInputStreamWithStart(ctx, repo, nil, io.Discard, start, "rev-parse", "HEAD")
		}},
	} {
		t.Run(runner.name, func(t *testing.T) {
			repo, _ := repo(t)
			manifest := configureFilter(t, repo, filterManifestFor(t, filterGitPath(t), filterTool(t, "clean"), filterTool(t, "smudge")))
			childPath := ""
			if runner.child {
				git(t, repo, "add", ".gitattributes")
				git(t, repo, "commit", "-m", "attributes")
				childPath = filepath.Join(t.TempDir(), "worktree")
				git(t, repo, "worktree", "add", "--detach", childPath, "HEAD")
			}
			source, err := OpenFilterTargetGit(context.Background(), repo, manifest)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			runContext := source.Context(context.Background())
			runDir := repo
			if runner.child {
				bound, e := source.BindWorktree(context.Background(), childPath)
				if e != nil {
					t.Fatal(e)
				}
				defer bound.Close()
				runContext, runDir = bound.Context(context.Background()), childPath
			}
			index := filepath.Join(repo, ".git", "index")
			original, err := os.ReadFile(index)
			if err != nil {
				t.Fatal(err)
			}
			journal := &filterTargetGuardJournal{afterIntent: func() {
				if err := os.WriteFile(index, append(original, 0), 0600); err != nil {
					t.Fatal(err)
				}
			}}
			starts := 0
			err = runner.run(WithCandidateJournal(runContext, journal), runDir, func(context.Context, process.Command) (*process.Handle, error) {
				starts++
				return nil, context.DeadlineExceeded
			})
			if err == nil {
				t.Fatal("index drift accepted")
			}
			if starts != 0 {
				t.Fatalf("Git start entered after intent index drift: %d", starts)
			}
		})
	}
}
