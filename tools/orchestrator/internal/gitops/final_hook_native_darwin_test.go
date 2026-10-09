//go:build darwin

package gitops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type finalHookJournal struct {
	memoryJournal
	mu      sync.Mutex
	records []GitProcessRecord
}

func (j *finalHookJournal) RecordGitProcess(_ context.Context, r GitProcessRecord) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if r.Process != nil {
		v := *r.Process
		r.Process = &v
	}
	j.records = append(j.records, r)
	return nil
}
func (j *finalHookJournal) snapshot() []GitProcessRecord {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]GitProcessRecord(nil), j.records...)
}
func (j *finalHookJournal) allWaited() bool {
	active := map[string]bool{}
	for _, r := range j.snapshot() {
		if r.Process == nil {
			continue
		}
		key := fmt.Sprintf("%d:%s", r.Process.PID, r.Process.Birth)
		switch r.Phase {
		case "spawned":
			active[key] = true
		case "exited":
			delete(active, key)
		case "unknown":
			return false
		}
	}
	return len(active) == 0
}

type finalHookFixture struct {
	root, repo, base, candidate, marker, hook, mergeDigest string
	request                                                IntegrationRequest
	journal                                                *finalHookJournal
	safe                                                   bool
}

func finalHookSetup(t *testing.T, body string) *finalHookFixture {
	t.Helper()
	if os.Getenv("FINAL_GIT_EXTENSION_NATIVE") != "1" {
		t.Skip("requires explicit native final Git acceptance")
	}
	_, src, _, ok := runtime.Caller(0)
	if !ok || !filepath.IsAbs(src) {
		t.Fatal("source location unavailable")
	}
	parent := filepath.Clean(filepath.Join(filepath.Dir(src), "../../../../tasks/final-git-extension-native/tmp"))
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(parent, "hook-")
	if err != nil {
		t.Fatal(err)
	}
	f := &finalHookFixture{root: root, repo: filepath.Join(root, "repo"), marker: filepath.Join(root, "entered"), hook: filepath.Join(root, "hooks", "post-merge"), journal: &finalHookJournal{}}
	t.Cleanup(func() {
		if !f.safe || t.Failed() || !f.journal.allWaited() {
			t.Logf("retained native Git fixture without complete successful original Wait/tree proof: %s", f.root)
			return
		}
		if err := os.RemoveAll(f.root); err != nil {
			t.Error(err)
		} else {
			t.Logf("removed exact owned fixture after all original Wait/tree proofs: %s", f.root)
		}
	})
	// These settings affect only this nonparallel test process. Clear inherited
	// Git overrides before enabling just the private local hook configuration.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			t.Setenv(key, "")
			if err = os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, dir := range []string{f.repo, filepath.Dir(f.hook), filepath.Join(root, "home")} {
		if err = os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "home"))
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	git(t, f.repo, "init", "-q", "-b", "main")
	git(t, f.repo, "config", "user.email", "native-hook@example.invalid")
	git(t, f.repo, "config", "user.name", "Native hook fixture")
	git(t, f.repo, "config", "gc.auto", "0")
	git(t, f.repo, "config", "maintenance.auto", "false")
	git(t, f.repo, "config", "core.hooksPath", filepath.Dir(f.hook))
	if err = os.WriteFile(filepath.Join(f.repo, "README"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, f.repo, "add", "README")
	git(t, f.repo, "commit", "-qm", "base")
	f.base = git(t, f.repo, "rev-parse", "HEAD")
	f.candidate, _ = candidate(t, f.repo)
	git(t, f.repo, "reset", "--hard", f.base)
	script := "#!/bin/sh\nprintf '%s\\n' \"$PPID\" > " + finalHookQuote(f.marker) + "\n" + body + "\n"
	if err = os.WriteFile(f.hook, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	f.request = IntegrationRequest{AttemptID: "native-final-hook", RepoRoot: f.repo, TargetWorktree: f.repo, TargetRef: "refs/heads/main", TargetBaseOID: f.base, CandidateOID: f.candidate, Review: reviewFor(t, f.repo, f.base, f.candidate), Journal: f.journal}
	args := []string{"-C", f.repo, "merge", "--ff-only", "--no-autostash", "--no-overwrite-ignore", f.candidate}
	f.mergeDigest = DigestDetail(f.repo + "\x00" + strings.Join(args, "\x00"))
	return f
}
func finalHookQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
func finalHookVerifyOriginalMerge(t *testing.T, f *finalHookFixture) {
	t.Helper()
	var started, exited *process.Identity
	starts, ends := 0, 0
	for _, r := range f.journal.snapshot() {
		if r.CommandDigest != f.mergeDigest {
			continue
		}
		if r.Phase == "spawned" {
			starts++
			started = r.Process
		}
		if r.Phase == "exited" {
			ends++
			exited = r.Process
		}
	}
	if starts != 1 || ends != 1 || started == nil || exited == nil || *started != *exited || !f.journal.allWaited() {
		t.Fatalf("merge lacks unique original Wait/tree journal: starts=%d exits=%d records=%+v", starts, ends, f.journal.snapshot())
	}
	marker, err := os.ReadFile(f.marker)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(marker)))
	if err != nil || pid != started.PID {
		t.Fatalf("hook parent does not match original merge: %q %+v %v", marker, started, err)
	}
	t.Logf("one original merge PID=%d birth=%s reached hook, Wait and ConfirmTreeExited; no second merge", started.PID, started.Birth)
	f.safe = true
}

func TestTrackedIntegrateNativePostMergeHooks(t *testing.T) {
	for _, mode := range []string{"normal", "tracked_write", "tracked_write_exit7"} {
		t.Run(mode, func(t *testing.T) {
			body := "exit 0"
			if mode != "normal" {
				body = "printf 'hook changed tracked content\\n' > README\nexit 0"
			}
			if mode == "tracked_write_exit7" {
				body = strings.ReplaceAll(body, "exit 0", "exit 7")
			}
			f := finalHookSetup(t, body)
			before, err := os.ReadFile(f.hook)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			out, err := TrackedIntegrate(WithCandidateJournal(ctx, f.journal), f.request)
			finalHookVerifyOriginalMerge(t, f)
			if git(t, f.repo, "rev-parse", "HEAD") != f.candidate {
				t.Fatal("real fast-forward did not reach candidate")
			}
			if len(f.journal.intents) != 1 || len(f.journal.outcomes) != 1 {
				t.Fatalf("missing durable-protocol callbacks: %+v", f.journal)
			}
			if mode == "normal" {
				if err != nil || out.State != IntegrationIntegrated || out.GitExitCode != 0 {
					t.Fatalf("normal hook: %+v %v", out, err)
				}
			} else {
				contents, readErr := os.ReadFile(filepath.Join(f.repo, "README"))
				if readErr != nil || string(contents) != "hook changed tracked content\n" {
					t.Fatal("hook tracked write not preserved", readErr)
				}
				if !errors.Is(err, ErrIntegrationUncertain) || out.State != IntegrationUncertain || f.journal.outcomes[0].State != IntegrationUncertain {
					t.Fatalf("hook change escaped post-check: %+v %v", out, err)
				}
				if git(t, f.repo, "status", "--porcelain") == "" {
					t.Fatal("changed target incorrectly clean")
				}
			}
			after, readErr := os.ReadFile(f.hook)
			if readErr != nil || string(before) != string(after) {
				t.Fatal("hook identity changed", readErr)
			}
			digest := sha256.Sum256(after)
			t.Logf("mode=%s hook_sha256=%s head=%s state=%s git_exit=%d", mode, hex.EncodeToString(digest[:]), out.TargetHEAD, out.State, out.GitExitCode)
		})
	}
}

func TestTrackedIntegrateNativeStartedMergeCancellation(t *testing.T) {
	f := finalHookSetup(t, "exec /bin/sleep 30")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type answer struct {
		out IntegrationOutcome
		err error
	}
	done := make(chan answer, 1)
	received := false
	go func() {
		out, err := TrackedIntegrate(WithCandidateJournal(ctx, f.journal), f.request)
		done <- answer{out, err}
	}()
	defer func() {
		if !received {
			cancel()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Logf("original integration remains unproven; fixture retained: %s", f.root)
			}
		}
	}()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if body, err := os.ReadFile(f.marker); err == nil && len(body) > 0 {
			break
		}
		select {
		case result := <-done:
			received = true
			t.Fatalf("merge exited before real hook started: %+v %v", result.out, result.err)
		case <-deadline.C:
			t.Fatal("real hook never started")
		case <-tick.C:
		}
	}
	canceledAt := time.Now()
	cancel()
	var result answer
	select {
	case result = <-done:
		received = true
	case <-time.After(15 * time.Second):
		t.Fatal("original integration did not finish after cancellation")
	}
	finalHookVerifyOriginalMerge(t, f)
	if !errors.Is(result.err, ErrIntegrationUncertain) || result.out.State != IntegrationUncertain || len(f.journal.intents) != 1 || len(f.journal.outcomes) != 1 || f.journal.outcomes[0].State != IntegrationUncertain {
		t.Fatalf("canceled started merge became retryable: %+v %v", result.out, result.err)
	}
	// The canceled context cannot prove a fresh post-read. Inspect using a fresh
	// test-owned Git command after the original merge Wait/tree has completed.
	head := git(t, f.repo, "rev-parse", "HEAD")
	status := git(t, f.repo, "status", "--porcelain")
	if head != f.candidate || status != "" {
		t.Fatalf("unexpected preserved target: head=%s status=%q", head, status)
	}
	t.Logf("canceled after hook entered; original Wait/tree latency=%s; separate post-observation head=%s clean=true; returned state=%s", time.Since(canceledAt), head, result.out.State)
}
