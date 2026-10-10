//go:build darwin

package gitops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
	after        func(MaterializeReceipt)
}

func (j *rawFixtureJournal) RecordFilterMaterialized(_ context.Context, r MaterializeReceipt) error {
	j.materialized = append(j.materialized, r)
	if j.after != nil {
		j.after(r)
	}
	return nil
}
func (j *rawFixtureJournal) RecordGitProcess(_ context.Context, r GitProcessRecord) error {
	j.records = append(j.records, r)
	return nil
}

// This double injects only the observed binding; stream operations still belong
// to the same real Broker. It tests rejection before any operation is reached.
type rawBindingObservation struct {
	FilterStreams
	value gitfilter.StreamBinding
}

func (s rawBindingObservation) Binding() gitfilter.StreamBinding { return s.value }

type rawFixture struct {
	t        *testing.T
	root     string
	retain   bool
	broker   gitfilter.ResourceBroker
	gitWaits int
	journal  *rawFixtureJournal
}

func newRawFixture(t *testing.T) *rawFixture {
	root, err := os.MkdirTemp(filepath.Join(moduleRoot(t), "../../tasks/filter-raw-fixture/tmp"), "raw-")
	if err != nil {
		t.Fatal(err)
	}
	f := &rawFixture{t: t, root: root}
	t.Cleanup(func() {
		if f.journal != nil {
			f.checkGitRecords(t)
		}
		if f.broker != nil {
			state := f.broker.State()
			if state.Unknown {
				f.retain = true
			}
			t.Logf("broker_terminal ever_started=%t unknown=%t started=%v terminal=%v", state.EverStarted, state.Unknown, state.StartedAt, state.TerminalAt)
			if !f.retain {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				receipt, e := f.broker.Cleanup(ctx)
				cancel()
				if e != nil || receipt == nil {
					f.retain = true
					t.Errorf("broker cleanup: %v", e)
				}
			}
			if e := f.broker.Close(); e != nil {
				f.retain = true
				t.Errorf("broker close: %v", e)
			}
		}
		t.Logf("fixture_git_original_wait_tree_success=%d root=%s", f.gitWaits, root)
		if f.retain {
			t.Logf("retained_unknown_fixture=%s", root)
			return
		}
		if e := os.RemoveAll(root); e != nil {
			t.Errorf("fixture cleanup: %v", e)
			return
		}
		if _, e := os.Lstat(root); !os.IsNotExist(e) {
			t.Errorf("fixture remains: %v", e)
		} else {
			t.Logf("owned_fixture_absent=%s", root)
		}
	})
	return f
}
func (f *rawFixture) checkGitRecords(t *testing.T) {
	t.Helper()
	records := f.journal.records
	if len(records)%3 != 0 {
		f.retain = true
		t.Errorf("unfinished Git process record count=%d", len(records))
		return
	}
	for i := 0; i < len(records); i += 3 {
		a, b, c := records[i], records[i+1], records[i+2]
		if a.Phase != "intent" || b.Phase != "spawned" || c.Phase != "exited" || a.CommandDigest != b.CommandDigest || a.CommandDigest != c.CommandDigest || b.Process == nil || c.Process == nil || *b.Process != *c.Process || c.ExitCode != 0 {
			f.retain = true
			t.Errorf("unproved Git lifecycle records %d: %s %s %s exit=%d", i, a.Phase, b.Phase, c.Phase, c.ExitCode)
			return
		}
	}
	t.Logf("materialize_original_git_wait_tree_success=%d", len(records)/3)
}
func rawFixtureWait(t *testing.T, f *rawFixture, ctx context.Context, h *process.Handle) {
	t.Helper()
	wait := h.Wait(ctx)
	tree := h.ConfirmTreeExited()
	if wait != nil || tree != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		stop := h.Stop(stopCtx)
		cancel()
		tree = h.ConfirmTreeExited()
		if stop != nil || tree != nil {
			f.retain = true
		}
		t.Fatalf("original process wait=%v stop=%v tree=%v", wait, stop, tree)
	}
	if h.ExitCode() != 0 {
		t.Fatalf("original process exit=%d output=%s", h.ExitCode(), h.Output())
	}
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
	if e != nil {
		f.retain = true
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = h.Stop(stopCtx)
		stopCancel()
		t.Fatal(e)
	}
	rawFixtureWait(t, f, ctx, h)
	t.Logf("actor_build_original_wait=0 exit=0 tree=0")
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
	if e != nil {
		f.retain = true
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = h.Stop(stopCtx)
		stopCancel()
		t.Fatal(e)
	}
	rawFixtureWait(t, f, ctx, h)
	f.gitWaits++
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
	for _, tc := range []struct {
		name, clean         string
		drift, journalDrift bool
	}{{"xor_roundtrip_receipt_and_controlled_file", "xor", false, false}, {"non_roundtrip_rejects_and_retains_workspace", "copy", false, false}, {"tree_attributes_drift_rejects_before_workspace", "xor", true, false}, {"journal_receipt_drift_rejected", "xor", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
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
			version := rawGit(t, f, ctx, gitPath, repo, "--version")
			m := contract.GitFilterManifestV1{Version: 1, DriverID: "sealed", Paths: []string{"a.txt"}, Git: contract.GitFilterGitPin{Path: gitPath, Version: version, SHA256: filterFileSHA256(t, gitPath)}, Clean: contract.GitFilterToolPin{Path: tool.Path, SHA256: tool.SHA256, Args: []string{tc.clean}}, Smudge: contract.GitFilterToolPin{Path: tool.Path, SHA256: tool.SHA256, Args: []string{"xor"}}}
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
			f.journal = j
			if tc.journalDrift {
				j.after = func(r MaterializeReceipt) {
					if err := os.WriteFile(filepath.Join(r.Worktree, "README"), []byte("changed\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.drift {
				broker, scope := rawBroker(t, f, ctx, m)
				_, e = MaterializeFiltered(ctx, source, broker, MaterializeRequest{RepoRoot: repo, Worktree: target, AttemptID: "a", BaseOID: head, CandidateOID: head, OrderedInputOIDs: []string{head}, PlanRevision: 1, FilterScopeSHA256: scope, FilterReservationID: "filter"}, j)
				if errors.Is(e, process.ErrProcessTreeUnknown) {
					f.retain = true
				}
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
				for _, kind := range []string{"scope", "reservation", "injected-manifest-observation"} {
					wrong := filepath.Join(f.root, "wrong-"+kind)
					request := MaterializeRequest{RepoRoot: repo, Worktree: wrong, AttemptID: "a", BaseOID: head, CandidateOID: head, OrderedInputOIDs: []string{head}, PlanRevision: 1, FilterScopeSHA256: filterScope, FilterReservationID: "filter"}
					var streams FilterStreams = broker
					switch kind {
					case "scope":
						request.FilterScopeSHA256 = strings.Repeat("0", 64)
					case "reservation":
						request.FilterReservationID = "other-reservation"
					default:
						value := broker.Binding()
						value.ManifestSHA256 = strings.Repeat("0", 64)
						streams = rawBindingObservation{FilterStreams: broker, value: value}
					}
					records, receipts := len(j.records), len(j.materialized)
					_, wrongErr := MaterializeFiltered(ctx, source, streams, request, j)
					if wrongErr == nil || broker.State().EverStarted || len(j.records) != records || len(j.materialized) != receipts {
						t.Fatalf("wrong %s reached materialization err=%v", kind, wrongErr)
					}
					if _, x := os.Lstat(wrong); !os.IsNotExist(x) {
						t.Fatalf("wrong %s target=%v", kind, x)
					}
				}
			}
			got, e := MaterializeFiltered(ctx, source, broker, MaterializeRequest{RepoRoot: repo, Worktree: target, AttemptID: "a", BaseOID: head, CandidateOID: head, OrderedInputOIDs: []string{head}, PlanRevision: 1, FilterScopeSHA256: filterScope, FilterReservationID: "filter"}, j)
			if got != nil {
				defer got.Close()
			}
			if errors.Is(e, process.ErrProcessTreeUnknown) {
				f.retain = true
			}
			if tc.clean == "copy" {
				state := broker.State()
				if !IsCode(e, CodeInvalidInput) || !state.EverStarted || state.Unknown || state.TerminalAt.IsZero() || len(j.materialized) != 0 {
					t.Fatalf("non-roundtrip outcome err=%v state=%+v", e, state)
				}
				if _, x := os.Lstat(target); x != nil {
					t.Fatalf("workspace not retained: %v", x)
				}
				return
			}
			if tc.journalDrift {
				if !IsCode(e, CodeIdentityChanged) || got != nil {
					t.Fatalf("journal drift returned_live=%t err=%v", got != nil, e)
				}
				if _, x := os.Lstat(target); x != nil {
					t.Fatalf("journal drift target: %v", x)
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
			if info, e := os.Stat(filepath.Join(target, "a.txt")); e != nil || info.Mode()&0111 != 0 {
				t.Fatalf("a.txt mode=%v err=%v", info, e)
			}
			if info, e := os.Stat(filepath.Join(target, "script")); e != nil || info.Mode()&0111 == 0 {
				t.Fatalf("script mode=%v err=%v", info, e)
			}
			r := got.Receipt()
			if r.CommitOID != head || r.Filter == nil || r.Filter.ValidateBinding(m, source.SHA256()) != nil {
				t.Fatalf("invalid receipt: %#v", r)
			}
			if len(r.Files) != 5 || len(r.Filter.Views) != 1 || r.Filter.Views[0].Path != "a.txt" || r.Filter.Views[0].CanonicalSHA256 != rawSHA(canonical) || r.Filter.Views[0].RenderedSHA256 != rawSHA(xorRaw(canonical)) || len(j.materialized) != 1 || len(j.records) < 3 {
				t.Fatalf("receipt/journal incomplete: %#v records=%d", r, len(j.records))
			}
			for _, p := range []string{".git", ".gitattributes", "README", "a.txt", "script"} {
				actual, e := fileIdentity(filepath.Join(target, p))
				if e != nil || r.Files[p] != actual {
					t.Fatalf("inventory %s err=%v", p, e)
				}
			}
			if !reflect.DeepEqual(r, j.materialized[0]) {
				t.Fatal("persisted receipt differs")
			}
			originalInputs := append([]string(nil), r.OrderedInputOIDs...)

			clone := r
			clone.Files["a.txt"] = FileIdentity{}
			clone.Filter.Views[0].Path = "changed"
			clone.OrderedInputOIDs[0] = "changed"
			after := got.Receipt()
			if after.Files["a.txt"] == (FileIdentity{}) || after.Filter.Views[0].Path != "a.txt" || !reflect.DeepEqual(after.OrderedInputOIDs, originalInputs) {
				t.Fatal("receipt was not cloned")
			}
			if e := got.Close(); e != nil {
				t.Fatal(e)
			}
			if e := got.Verify(ctx); e == nil {
				t.Fatal("closed materialization verified")
			}
			if e := source.Verify(ctx); e != nil {
				t.Fatal(e)
			}
			var slot bytes.Buffer
			if e := broker.ReadSlot(ctx, 1, &slot); e != nil || !bytes.Equal(slot.Bytes(), xorRaw(canonical)) {
				t.Fatalf("materialization Close changed broker: %v", e)
			}
			if broker.State().Unknown {
				f.retain = true
				t.Fatal("broker unknown")
			}
		})
	}
}
