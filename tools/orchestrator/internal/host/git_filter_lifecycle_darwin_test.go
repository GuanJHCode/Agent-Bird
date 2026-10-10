//go:build darwin

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitfilter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestFilterHostOriginalLifecycle(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Clean(filepath.Join(cwd, "../../../../tasks/filter-host-lifecycle/tmp"))
	if err = os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "native-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	pending := 0
	retain := false
	// Failed assertions may still be cleaned when every actual original process
	// has Wait/tree proof. Unproved processes always retain the owned fixture.
	t.Cleanup(func() {
		if retain || pending != 0 {
			t.Logf("RETAINED root=%s pending=%d", root, pending)
			return
		}
		now, e := os.Lstat(root)
		if e != nil || !os.SameFile(rootInfo, now) {
			t.Errorf("fixture root identity: %v", e)
			return
		}
		if e = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return os.Chmod(p, 0700)
			}
			return nil
		}); e != nil {
			t.Error(e)
			return
		}
		if e = os.RemoveAll(root); e != nil {
			t.Error(e)
		} else {
			t.Logf("owned native fixture removed=%s pending=0", root)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	tool := filepath.Join(root, "actor")
	pending++
	builder, err := process.Start(ctx, process.Command{Path: "/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go", Args: []string{"build", "-o", tool, "../gitfilter/testdata/actor"}, Dir: cwd, Env: []string{"TMPDIR=/private/tmp"}})
	if err != nil || builder == nil {
		t.Fatalf("original actor build Start: %v", err)
	}
	we, te := builder.Wait(ctx), builder.ConfirmTreeExited()
	if we != nil || te != nil {
		t.Fatalf("original actor build Wait/tree: %v %v", we, te)
	}
	pending--
	t.Logf("actor build original Wait/tree=true exit=%d", builder.ExitCode())
	if builder.ExitCode() != 0 {
		t.Fatal(builder.Output())
	}
	if err = os.Chmod(tool, 0500); err != nil {
		t.Fatal(err)
	}
	pin, err := process.ExecutableDigest(ctx, tool)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"prepare_provider_freeze", "failed_prepare_filter", "never_started", "cancel_after_filter", "unproved_provider_start", "journal_unknown"} {
		t.Run(mode, func(t *testing.T) {
			dir := filepath.Join(root, mode)
			if e := os.Mkdir(dir, 0700); e != nil {
				t.Fatal(e)
			}
			h, e := NewIPC(filepath.Join(dir, "spool"), "host")
			if e != nil {
				t.Fatal(e)
			}
			filterMode := "copy"
			if mode == "failed_prepare_filter" {
				filterMode = "fail"
			}
			m := contract.GitFilterManifestV1{Version: 1, DriverID: "fixture", Paths: []string{"file"}, Git: contract.GitFilterGitPin{Path: tool, Version: "fixture", SHA256: pin}, Clean: contract.GitFilterToolPin{Path: tool, SHA256: pin, Args: []string{filterMode}}, Smudge: contract.GitFilterToolPin{Path: tool, SHA256: pin, Args: []string{filterMode}}, Attributes: []contract.GitFilterAttributePin{}, ConfigSHA256: strings.Repeat("a", 64)}
			raw, e := json.Marshal(map[string]any{"kind": "candidate", "candidate_action": map[string]any{"version": 1, "operation": "validate"}, "git_filter": m})
			if e != nil {
				t.Fatal(e)
			}
			g := withHostArtifactsForTest(t, contract.LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", CommandID: "command", HostID: "host", PlanRevision: 1, WorkRevision: 1, ExecutionEpoch: 1, AdapterPayload: raw})
			sha, _ := m.Digest()
			scope, _ := contract.GitFilterResourceScopeSHA256(g, sha)
			g.GitFilter = &contract.GitFilterResourceGrant{Version: 1, ReservationID: "filter", ScopeSHA256: scope, ManifestSHA256: sha, CleanToolSHA256: pin, SmudgeToolSHA256: pin, ToolMaximumBytes: contract.GitFilterToolMaximumBytes, StreamMaximumBytes: contract.GitFilterStreamMaximumBytes, StreamSlots: 3}
			f, e := newFilterRuntime(g)
			if e != nil {
				t.Fatal(e)
			}
			meta := metadataForLaunch(g)
			meta.filter = f
			inputPath := filepath.Join(dir, "input")
			if e = os.WriteFile(inputPath, []byte("content\n"), 0600); e != nil {
				t.Fatal(e)
			}
			input, e := os.Open(inputPath)
			if e != nil {
				t.Fatal(e)
			}
			defer input.Close()
			runCtx, runCancel := context.WithCancel(ctx)
			defer runCancel()
			var first, last gitfilter.Result
			transform := func(c context.Context, src, dst int) (gitfilter.Result, error) {
				r, e := f.broker.Transform(c, "clean", src, dst)
				t.Logf("filter started=%t original_wait=%t tree=%t unknown=%t exit=%d", r.Started, r.WaitProved, r.TreeProved, r.Unknown, r.ExitCode)
				if r.Started && (!r.WaitProved || !r.TreeProved) {
					retain = true
				}
				return r, e
			}
			command := process.Command{Path: tool, Args: []string{"copy"}, Dir: dir, Stdin: []byte("provider result"), PinnedPath: tool, PinnedSHA256: pin}
			var provider *process.Handle
			meta.startProcess = func(c context.Context, cmd process.Command) (*process.Handle, error) {
				if mode == "unproved_provider_start" {
					return nil, errors.New("injected_unproved_start")
				}
				pending++
				var e error
				provider, e = process.Start(c, cmd)
				return provider, e
			}
			meta.prepareCandidate = func(c context.Context) (process.Command, func(context.Context, string) ([]byte, error), func(), error) {
				if f.broker == nil {
					return command, nil, func() {}, errors.New("filter_broker_not_opened")
				}
				if mode == "never_started" {
					return command, nil, func() {}, errors.New("preparation_rejected")
				}
				if e := f.broker.Load(c, 0, input); e != nil {
					return command, nil, func() {}, e
				}
				var e error
				first, e = transform(c, 0, 1)
				if e != nil {
					return command, nil, func() {}, e
				}
				if mode == "cancel_after_filter" {
					runCancel()
					return command, nil, func() {}, context.Canceled
				}
				if mode == "journal_unknown" {
					if e = os.WriteFile(filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, ".git-filter.json.tmp"), []byte("foreign"), 0600); e != nil {
						t.Fatal(e)
					}
					_, e = transform(c, 1, 2)
					return command, nil, func() {}, e
				}
				freeze := func(c context.Context, result string) ([]byte, error) {
					var e error
					last, e = transform(c, 1, 2)
					return []byte(result), e
				}
				return command, freeze, func() {}, nil
			}
			a := store.Attempt{ID: g.AttemptID, TaskID: g.TaskID, SegmentID: g.SegmentID, Status: "running"}
			result, runErr := h.execute(runCtx, a, g.RunID, g.TaskID, command, false, meta)
			if provider != nil {
				proofCtx, proofCancel := context.WithTimeout(context.Background(), 10*time.Second)
				w, tree := provider.Wait(proofCtx), provider.ConfirmTreeExited()
				proofCancel()
				if w == nil && tree == nil {
					pending--
				} else {
					retain = true
				}
				t.Logf("provider original Wait=%v tree=%v exit=%d", w, tree, provider.ExitCode())
				if w != nil || tree != nil {
					t.Fatalf("original provider proof missing: %v %v", w, tree)
				}
			}
			events, e := h.Collect(ctx, a)
			if e != nil {
				t.Fatal(e)
			}
			kinds := make([]string, 0, len(events))
			var terminal *contract.Event
			for i := range events {
				kinds = append(kinds, events[i].Kind)
				if events[i].Kind == contract.EventExited || events[i].Kind == contract.EventLaunchRejected || events[i].Kind == contract.EventUnknown {
					terminal = &events[i]
				}
			}
			t.Logf("result=%s error=%v events=%v", result.Status, runErr, kinds)
			if mode == "journal_unknown" || mode == "unproved_provider_start" {
				if result.Status != "unknown" || terminal == nil || terminal.Kind != contract.EventUnknown || terminal.GitFilterCleanup != nil || terminal.ActiveMS != 0 {
					t.Fatalf("uncertainty settled: %+v %+v", result, terminal)
				}
				if _, e = os.Lstat(filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, ".git-filter")); e != nil {
					t.Fatal("unknown resources removed", e)
				}
				return
			}
			if terminal == nil || terminal.GitFilterCleanup == nil || terminal.GitFilterCleanup.Validate() != nil {
				t.Fatalf("known terminal lacks cleanup: %+v", terminal)
			}
			if _, e = os.Lstat(filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, ".git-filter")); !os.IsNotExist(e) {
				t.Fatal("known resources retained", e)
			}
			if mode == "never_started" {
				if terminal.Kind != contract.EventLaunchRejected || terminal.ActiveMS != 0 || provider != nil {
					t.Fatalf("false start proof: %+v", terminal)
				}
				return
			}
			if terminal.Kind != contract.EventExited || terminal.ActiveMS < 1 {
				t.Fatalf("started filter misclassified: %+v", terminal)
			}
			for _, k := range kinds {
				if k == contract.EventLaunchRejected {
					t.Fatal("filter ran before never-started declaration")
				}
			}
			if mode == "prepare_provider_freeze" {
				if runErr != nil || result.Status != "result_ready" || provider == nil || provider.OriginalSpawnedAt().IsZero() {
					t.Fatalf("successful live span: %+v %v", result, runErr)
				}
				if !provider.OriginalSpawnedAt().After(first.TerminalAt) || !last.StartedAt.After(provider.OriginalSpawnedAt()) {
					t.Fatal("not a real prepare/provider/freeze sequence")
				}
				expected := last.TerminalAt.Sub(first.StartedAt).Milliseconds()
				if expected < 1 {
					expected = 1
				}
				if terminal.ActiveMS != expected {
					t.Fatalf("span omitted preparation/freeze or gaps: active=%d expected=%d", terminal.ActiveMS, expected)
				}
			} else {
				if result.Status != "failed" || provider != nil || len(kinds) < 3 || kinds[len(kinds)-2] != contract.EventFailed {
					t.Fatalf("pre-provider failure lost: %+v %v", result, kinds)
				}
			}
		})
	}
}
