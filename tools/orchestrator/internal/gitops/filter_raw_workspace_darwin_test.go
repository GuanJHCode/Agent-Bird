//go:build darwin

package gitops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
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

type rawFixture struct {
	t      *testing.T
	root   string
	retain bool
	broker gitfilter.ResourceBroker
}

func newRawFixture(t *testing.T) *rawFixture {
	root, err := os.MkdirTemp(filepath.Join(moduleRoot(t), "../../tasks/filter-raw-fixture/tmp"), "raw-")
	if err != nil {
		t.Fatal(err)
	}
	f := &rawFixture{t: t, root: root}
	t.Cleanup(func() {
		if f.broker != nil {
			if f.broker.State().Unknown {
				f.retain = true
			} else {
				_, e := f.broker.Cleanup(context.Background())
				if e != nil {
					f.retain = true
				}
				_ = f.broker.Close()
			}
		}
		if f.retain {
			t.Logf("retained_unknown_fixture=%s", root)
		} else {
			_ = os.RemoveAll(root)
		}
	})
	return f
}
func moduleRoot(t *testing.T) string {
	t.Helper()
	d, e := os.Getwd()
	if e != nil {
		t.Fatal(e)
	}
	return filepath.Clean(filepath.Join(d, "../.."))
}
func buildRawActor(t *testing.T, f *rawFixture) gitfilter.Tool {
	t.Helper()
	goPath := filepath.Join(runtime.GOROOT(), "bin", "go")
	var e error
	goPath, e = filepath.EvalSymlinks(goPath)
	if e != nil {
		t.Fatal(e)
	}
	sha, e := process.ExecutableDigest(context.Background(), goPath)
	if e != nil {
		t.Fatal(e)
	}
	actor := filepath.Join(f.root, "xor-actor")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h, e := process.Start(ctx, process.Command{Path: goPath, PinnedPath: goPath, PinnedSHA256: sha, Args: []string{"build", "-trimpath", "-buildvcs=false", "-o", actor, "./internal/gitfilter/testdata/actor"}, Dir: moduleRoot(t), ExactEnv: true, Env: []string{"PATH=" + filepath.Dir(goPath) + ":/usr/bin:/bin", "HOME=" + f.root, "GOCACHE=" + filepath.Join(f.root, "cache"), "GOPATH=" + filepath.Join(f.root, "gopath"), "GOENV=off", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "TMPDIR=" + f.root}})
	if h == nil {
		t.Fatal(e)
	}
	wait := h.Wait(ctx)
	tree := h.ConfirmTreeExited()
	t.Logf("actor_build_original_wait=%v exit=%d tree=%v", wait, h.ExitCode(), tree)
	if e != nil || wait != nil || tree != nil || h.ExitCode() != 0 {
		f.retain = true
		t.Fatalf("actor build: %v %s", e, h.Output())
	}
	info, e := os.Stat(actor)
	if e != nil {
		t.Fatal(e)
	}
	digest, e := process.ExecutableDigest(context.Background(), actor)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(actor, 0500); e != nil {
		t.Fatal(e)
	}
	return gitfilter.Tool{Path: actor, SHA256: digest, Size: info.Size()}
}
func rawGit(t *testing.T, f *rawFixture, ctx context.Context, gitPath, dir string, args ...string) string {
	t.Helper()
	sha, e := process.ExecutableDigest(ctx, gitPath)
	if e != nil {
		t.Fatal(e)
	}
	h, e := process.Start(ctx, process.Command{Path: gitPath, PinnedPath: gitPath, PinnedSHA256: sha, Args: append(filterGitConfigArgs("sealed"), append([]string{"-C", dir}, args...)...), ExactEnv: true, Env: filterGitEnv()})
	if h == nil {
		t.Fatal(e)
	}
	w := h.Wait(ctx)
	tree := h.ConfirmTreeExited()
	if e != nil || w != nil || tree != nil || h.ExitCode() != 0 {
		f.retain = true
		t.Fatalf("git %v: start=%v wait=%v tree=%v exit=%d out=%s", args, e, w, tree, h.ExitCode(), h.Output())
	}
	return strings.TrimSpace(h.Output())
}
func rawSHA(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func xorRaw(b []byte) []byte {
	out := append([]byte(nil), b...)
	for i := range out {
		out[i] ^= 0x5a
	}
	return out
}
func rawConfigure(t *testing.T, f *rawFixture, ctx context.Context, gitPath, repo string, m contract.GitFilterManifestV1) contract.GitFilterManifestV1 {
	rawGit(t, f, ctx, gitPath, repo, "config", "filter."+m.DriverID+".clean", filterShellCommand(m.Clean))
	rawGit(t, f, ctx, gitPath, repo, "config", "filter."+m.DriverID+".smudge", filterShellCommand(m.Smudge))
	rawGit(t, f, ctx, gitPath, repo, "config", "filter."+m.DriverID+".required", "false")
	attrs := []byte("a.txt filter=" + m.DriverID + "\n")
	if e := os.WriteFile(filepath.Join(repo, ".gitattributes"), attrs, 0600); e != nil {
		t.Fatal(e)
	}
	m.Attributes = []contract.GitFilterAttributePin{{Path: ".gitattributes", SHA256: rawSHA(attrs)}}
	m.ConfigSHA256 = FilterConfigurationSHA256(m)
	return m
}
func rawBroker(t *testing.T, f *rawFixture, ctx context.Context, m contract.GitFilterManifestV1) (gitfilter.ResourceBroker, string) {
	segment := filepath.Join(f.root, "segments", "a", "s")
	sp, e := events.Open(segment)
	if e != nil {
		t.Fatal(e)
	}
	digest, e := m.Digest()
	if e != nil {
		t.Fatal(e)
	}
	launch := contract.LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "a", SegmentID: "s", HostID: "host", CommandID: "command", WorkRevision: 1, PlanRevision: 1}
	hs, e := contract.HostArtifactScopeSHA256(launch)
	if e != nil {
		t.Fatal(e)
	}
	launch.HostArtifacts = &contract.HostArtifactGrant{Version: 1, TerminalReservationID: "terminal", ProgressReservationID: "progress", ScopeSHA256: hs}
	fs, e := contract.GitFilterResourceScopeSHA256(launch, digest)
	if e != nil {
		t.Fatal(e)
	}
	b, e := gitfilter.OpenBroker(ctx, gitfilter.BrokerRequest{Launch: launch, Grant: contract.GitFilterResourceGrant{Version: 1, ReservationID: "filter", ScopeSHA256: fs, ManifestSHA256: digest, CleanToolSHA256: m.Clean.SHA256, SmudgeToolSHA256: m.Smudge.SHA256, ToolMaximumBytes: contract.GitFilterToolMaximumBytes, StreamMaximumBytes: contract.GitFilterStreamMaximumBytes, StreamSlots: 3}, Manifest: m, SegmentDirectory: segment, Spool: sp})
	if b != nil {
		f.broker = b
	}
	if e != nil {
		if b != nil && b.State().Unknown {
			f.retain = true
		}
		t.Fatal(e)
	}
	return b, fs
}
func TestFilterRawMaterializationNativeFixture(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, tc := range []struct {
		name, clean string
		drift       bool
	}{{"xor_roundtrip_receipt_and_controlled_file", "xor", false}, {"non_roundtrip_rejects_and_retains_workspace", "copy", false}, {"tree_attributes_drift_rejects_before_workspace", "xor", true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRawFixture(t)
			if e := os.MkdirAll(filepath.Join(f.root, "repo"), 0700); e != nil {
				t.Fatal(e)
			}
			tool := buildRawActor(t, f)
			gitPath := filterGitPath(t)
			repo := filepath.Join(f.root, "repo")
			rawGit(t, f, ctx, gitPath, repo, "init", "-q", "-b", "main")
			rawGit(t, f, ctx, gitPath, repo, "config", "user.name", "fixture")
			rawGit(t, f, ctx, gitPath, repo, "config", "user.email", "fixture@example.invalid")
			canonical := []byte("canonical\x00B\n")
			if e := os.WriteFile(filepath.Join(repo, "a.txt"), canonical, 0600); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(repo, "README"), []byte("control\n"), 0600); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(repo, "script"), []byte("#!/bin/true\n"), 0700); e != nil {
				t.Fatal(e)
			}
			m := filterManifestFor(t, gitPath, tool.Path, tool.Path)
			m.Clean.Args = []string{tc.clean}
			m.Smudge.Args = []string{"xor"}
			m.ConfigSHA256 = FilterConfigurationSHA256(m)
			m = rawConfigure(t, f, ctx, gitPath, repo, m)
			rawGit(t, f, ctx, gitPath, repo, "add", "a.txt", "README", "script", ".gitattributes")
			rawGit(t, f, ctx, gitPath, repo, "commit", "-q", "-m", "fixture")
			head := rawGit(t, f, ctx, gitPath, repo, "rev-parse", "HEAD")
			m = rawConfigure(t, f, ctx, gitPath, repo, m)
			if tc.drift {
				attrs := []byte("# same driver, different source bytes\na.txt filter=sealed\n")
				if e := os.WriteFile(filepath.Join(repo, ".gitattributes"), attrs, 0600); e != nil {
					t.Fatal(e)
				}
				m.Attributes[0].SHA256 = rawSHA(attrs)
				m.ConfigSHA256 = FilterConfigurationSHA256(m)
			}
			source, e := OpenFilterGit(ctx, repo, m)
			if e != nil {
				t.Fatal(e)
			}
			defer source.Close()
			target := filepath.Join(f.root, "out-"+strings.ReplaceAll(tc.name, "_", "-"))
			j := &rawFixtureJournal{}
			if tc.drift {
				broker, scope := rawBroker(t, f, ctx, m)
				_, e = MaterializeFiltered(ctx, source, broker, MaterializeRequest{RepoRoot: repo, Worktree: target, AttemptID: "a", BaseOID: head, CandidateOID: head, OrderedInputOIDs: []string{head}, PlanRevision: 1, FilterScopeSHA256: scope, FilterReservationID: "filter"}, j)
				if !IsCode(e, CodeIdentityChanged) {
					t.Fatalf("attributes drift error=%v", e)
				}
				if _, x := os.Lstat(target); !os.IsNotExist(x) {
					t.Fatalf("target exists: %v", x)
				}
				if broker.State().EverStarted {
					t.Fatal("attribute rejection started broker")
				}
				return
			}
			broker, filterScope := rawBroker(t, f, ctx, m)
			if tc.clean == "xor" {
				wrong := filepath.Join(f.root, "wrong-scope")
				_, wrongErr := MaterializeFiltered(ctx, source, broker, MaterializeRequest{RepoRoot: repo, Worktree: wrong, AttemptID: "a", BaseOID: head, CandidateOID: head, OrderedInputOIDs: []string{head}, PlanRevision: 1, FilterScopeSHA256: strings.Repeat("0", 64), FilterReservationID: "filter"}, j)
				if wrongErr == nil {
					t.Fatal("wrong scope accepted")
				}
				if _, x := os.Lstat(wrong); !os.IsNotExist(x) {
					t.Fatalf("wrong scope target: %v", x)
				}
			}
			got, e := MaterializeFiltered(ctx, source, broker, MaterializeRequest{RepoRoot: repo, Worktree: target, AttemptID: "a", BaseOID: head, CandidateOID: head, OrderedInputOIDs: []string{head}, PlanRevision: 1, FilterScopeSHA256: filterScope, FilterReservationID: "filter"}, j)
			if tc.clean == "copy" {
				if e == nil {
					t.Fatal("non-roundtrip accepted")
				}
				if _, x := os.Lstat(target); x != nil {
					t.Fatalf("workspace not retained: %v", x)
				}
				return
			}
			if e != nil || got == nil {
				t.Fatalf("materialize: got=%v err=%v", got, e)
			}
			a, e := os.ReadFile(filepath.Join(target, "a.txt"))
			if e != nil || !bytes.Equal(a, xorRaw(canonical)) {
				t.Fatalf("a.txt=%q err=%v", a, e)
			}
			readme, e := os.ReadFile(filepath.Join(target, "README"))
			if e != nil || string(readme) != "control\n" {
				t.Fatalf("README=%q err=%v", readme, e)
			}
			if info, e := os.Stat(filepath.Join(target, "script")); e != nil || info.Mode()&0111 == 0 {
				t.Fatalf("script mode=%v err=%v", info, e)
			}
			r := got.Receipt()
			if r.CommitOID != head || r.Filter == nil || r.Filter.ValidateBinding(m, source.SHA256()) != nil {
				t.Fatalf("invalid receipt: %#v", r)
			}
			if len(r.Files) == 0 || len(r.Filter.Views) != 1 || r.Filter.Views[0].Path != "a.txt" || r.Filter.Views[0].CanonicalSHA256 != rawSHA(canonical) || r.Filter.Views[0].RenderedSHA256 != rawSHA(xorRaw(canonical)) || len(j.materialized) != 1 || len(j.records) < 3 {
				t.Fatalf("receipt/journal incomplete: %#v records=%d", r, len(j.records))
			}
			clone := r
			clone.Files["a.txt"] = FileIdentity{}
			clone.Filter.Views[0].Path = "changed"
			after := got.Receipt()
			if after.Files["a.txt"] == (FileIdentity{}) || after.Filter.Views[0].Path != "a.txt" {
				t.Fatal("receipt was not cloned")
			}
			if e := got.Close(); e != nil {
				t.Fatal(e)
			}
			if e := source.Verify(ctx); e != nil {
				t.Fatal(e)
			}
			if broker.State().Unknown {
				f.retain = true
				t.Fatal("broker unknown")
			}
		})
	}
}
