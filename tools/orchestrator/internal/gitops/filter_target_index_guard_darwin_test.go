//go:build darwin

package gitops

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

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
		name string
		run  func(context.Context, string, func(context.Context, process.Command) (*process.Handle, error)) error
	}{
		{"bytes", func(ctx context.Context, repo string, start func(context.Context, process.Command) (*process.Handle, error)) error {
			_, err := candidateGitWithStart(ctx, repo, nil, start, "rev-parse", "HEAD")
			return err
		}},
		{"stream", func(ctx context.Context, repo string, start func(context.Context, process.Command) (*process.Handle, error)) error {
			return candidateGitInputStreamWithStart(ctx, repo, nil, io.Discard, start, "rev-parse", "HEAD")
		}},
	} {
		t.Run(runner.name, func(t *testing.T) {
			repo, _ := repo(t)
			manifest := configureFilter(t, repo, filterManifestFor(t, filterGitPath(t), filterTool(t, "clean"), filterTool(t, "smudge")))
			source, err := OpenFilterTargetGit(context.Background(), repo, manifest)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
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
			err = runner.run(source.Context(WithCandidateJournal(context.Background(), journal)), repo, func(context.Context, process.Command) (*process.Handle, error) {
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
