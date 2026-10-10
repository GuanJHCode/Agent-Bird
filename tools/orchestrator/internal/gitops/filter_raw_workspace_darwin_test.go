//go:build darwin

package gitops

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitfilter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type rawFixtureJournal struct {
	memoryJournal
	materialized []MaterializeReceipt
	records      []GitProcessRecord
}

func (j *rawFixtureJournal) RecordFilterMaterialized(_ context.Context, r MaterializeReceipt) error {
	j.materialized = append(j.materialized, r)
	return nil
}
func (j *rawFixtureJournal) RecordGitProcess(_ context.Context, r GitProcessRecord) error {
	j.records = append(j.records, r)
	return nil
}

type rawFixtureStreams struct{}

func (rawFixtureStreams) Load(context.Context, int, *os.File) error {
	return errors.New("fixture Load unused")
}
func (rawFixtureStreams) LoadStream(context.Context, int, func(io.Writer) error) (gitfilter.StreamLoadResult, error) {
	return gitfilter.StreamLoadResult{}, errors.New("fixture LoadStream unused")
}
func (rawFixtureStreams) Transform(context.Context, string, int, int) (gitfilter.Result, error) {
	return gitfilter.Result{}, errors.New("fixture Transform unused")
}
func (rawFixtureStreams) ReadSlot(context.Context, int, io.Writer) error {
	return errors.New("fixture ReadSlot unused")
}

func buildRawActor(t *testing.T, root string) gitfilter.Tool {
	t.Helper()
	goPath := filepath.Join(runtime.GOROOT(), "bin", "go")
	var err error
	goPath, err = filepath.EvalSymlinks(goPath)
	if err != nil {
		t.Fatal(err)
	}
	sha, err := process.ExecutableDigest(context.Background(), goPath)
	if err != nil {
		t.Fatal(err)
	}
	actor := filepath.Join(root, "xor-actor")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h, err := process.Start(ctx, process.Command{Path: goPath, PinnedPath: goPath, PinnedSHA256: sha, Args: []string{"build", "-trimpath", "-buildvcs=false", "-o", actor, "./internal/gitfilter/testdata/actor"}, Dir: moduleRoot(t), ExactEnv: true, Env: []string{"PATH=" + filepath.Dir(goPath) + ":/usr/bin:/bin", "HOME=" + root, "GOCACHE=" + filepath.Join(root, "cache"), "GOPATH=" + filepath.Join(root, "gopath"), "GOENV=off", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "TMPDIR=" + root}})
	if h == nil {
		t.Fatalf("actor start: %v", err)
	}
	wait := h.Wait(ctx)
	tree := h.ConfirmTreeExited()
	t.Logf("actor_build_original_wait=%v exit=%d tree=%v", wait, h.ExitCode(), tree)
	if err != nil || wait != nil || tree != nil || h.ExitCode() != 0 {
		t.Fatalf("actor build: %v %s", err, h.Output())
	}
	info, err := os.Stat(actor)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := process.ExecutableDigest(context.Background(), actor)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(actor, 0500); err != nil {
		t.Fatal(err)
	}
	return gitfilter.Tool{Path: actor, SHA256: digest, Size: info.Size()}
}
func moduleRoot(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(d, "../.."))
}
func rawActor(t *testing.T, ctx context.Context, tool gitfilter.Tool, mode string, in []byte) []byte {
	t.Helper()
	root := filepath.Dir(tool.Path)
	input, err := os.CreateTemp(root, "in-")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err = input.Write(in); err != nil {
		t.Fatal(err)
	}
	out, err := os.CreateTemp(root, "out-")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	r, err := gitfilter.Execute(ctx, gitfilter.Request{Operation: "smudge", ScopeSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Tool: tool, Args: []string{mode}, Input: input, Output: out, EmptyDirectory: root})
	if err != nil || !r.Accepted || !r.WaitProved || !r.TreeProved {
		t.Fatalf("actor %s: %+v %v", mode, r, err)
	}
	b, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFilterRawMaterializationNativeFixture(t *testing.T) {
	root, err := os.MkdirTemp(filepath.Join(moduleRoot(t), "../../tasks/filter-raw-fixture/tmp"), "raw-")
	if err != nil {
		t.Fatal(err)
	}
	retain := false
	t.Cleanup(func() {
		if retain {
			t.Logf("retained_unknown_fixture=%s", root)
			return
		}
		_ = os.RemoveAll(root)
	})
	if err = os.MkdirAll(filepath.Join(root, "cache"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(root, "gopath"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	tool := buildRawActor(t, root)
	for _, tc := range []struct {
		name, clean string
		attrDrift   bool
	}{{"xor_roundtrip_receipt_and_controlled_file", "xor", false}, {"non_roundtrip_rejects_and_retains_workspace", "copy", false}, {"tree_attributes_drift_rejects_before_workspace", "xor", true}} {
		t.Run(tc.name, func(t *testing.T) {
			source, head := repo(t)
			canonical := []byte("canonical\x00B\n")
			if err := os.WriteFile(filepath.Join(source, "a.txt"), canonical, 0600); err != nil {
				t.Fatal(err)
			}
			git(t, source, "add", "a.txt")
			git(t, source, "commit", "-m", "canonical")
			head = git(t, source, "rev-parse", "HEAD")
			m := filterManifestFor(t, filterGitPath(t), tool.Path, tool.Path)
			m.Clean.SHA256 = tool.SHA256
			m.Smudge.SHA256 = tool.SHA256
			m.Clean.Args = []string{"xor"}
			m.Smudge.Args = []string{"xor"}
			m = configureFilter(t, source, m)
			git(t, source, "add", ".gitattributes")
			git(t, source, "commit", "-m", "attrs")
			head = git(t, source, "rev-parse", "HEAD")
			if tc.attrDrift {
				if err := os.WriteFile(filepath.Join(source, ".gitattributes"), []byte("a.txt filter=other\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			g, err := OpenFilterGit(ctx, source, m)
			if tc.attrDrift {
				if err == nil {
					t.Fatal("tree attrs accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			rendered := rawActor(t, ctx, tool, "xor", canonical)
			if bytes.Equal(rendered, canonical) || !bytes.Equal(rawActor(t, ctx, tool, "xor", rendered), canonical) {
				t.Fatal("native XOR roundtrip missing")
			}
			target := filepath.Join(root, t.Name())
			j := &rawFixtureJournal{}
			got, err := MaterializeFiltered(ctx, g, rawFixtureStreams{}, MaterializeRequest{RepoRoot: source, Worktree: target, AttemptID: "a", BaseOID: head, CandidateOID: head, PlanRevision: 1}, j)
			if tc.clean == "xor" {
				if err != nil || got == nil {
					t.Fatalf("materialize success=%v err=%v", got, err)
				}
				return
			}
			if err == nil {
				t.Fatal("non-roundtrip accepted")
			}
			if _, e := os.Lstat(target); e != nil {
				t.Fatalf("rejected workspace not retained: %v", e)
			}
		})
	}
}
