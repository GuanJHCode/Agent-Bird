//go:build darwin

package gitfilter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestCarrierHelperForeignHostBeforeBody(t *testing.T) {
	tr, ctx := transportFixture(t)
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	sha, err := process.ExecutableDigest(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	route, err := tr.helperRoute(ctx, CarrierHelperExecutableExpectation{path, sha})
	if err != nil {
		t.Fatal(err)
	}
	if e := helperNamed(route); e != nil {
		t.Fatalf("positive named control: %v", e)
	}
	if e := helperExecutable(ctx, route.Helper); e != nil {
		t.Fatalf("positive executable control: %v", e)
	}
	route.Host.PID++
	in, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	defer writer.Close()
	out, ow, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	defer ow.Close()
	payload := []byte("caller-private-content")
	if _, err = writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- tr.serve() }()
	if err = RunCarrierHelper(ctx, route, in, ow); err == nil {
		t.Fatal("foreign Host accepted")
	}
	if !errors.Is(err, process.ErrProcessTreeUnknown) || !strings.Contains(err.Error(), "helper_host_expectation") {
		t.Fatalf("wrong rejection boundary: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err = in.Read(got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("authentication consumed body: %q %v", got, err)
	}
	if err = tr.drain(ctx); err != nil {
		// The rejected helper may disconnect before the server obtains its
		// kernel credentials. That uncertainty must retain nodes and release
		// FDs, not be relabeled as a known successful drain.
		if !tr.gate.State().Unknown || !tr.socket.state.unknown || !tr.socket.state.released {
			t.Fatalf("unproved failed drain: %v gate=%+v", err, tr.gate.State())
		}
	}
	_ = gateResult(t, done)
	if tr.gate.State().Calls != 0 {
		t.Fatal("unauthenticated Exchange")
	}
	t.Log("foreign_host_rejected=true git_body_unread=true exchange_calls=0")
}

func transportOriginalCommand(t *testing.T, ctx context.Context, spec process.Command) *process.Handle {
	t.Helper()
	h, e := process.Start(ctx, spec)
	if h != nil {
		t.Cleanup(func() {
			c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = h.Stop(c)
			if e := h.Wait(c); e != nil {
				t.Error(e)
			}
			if e := h.ConfirmTreeExited(); e != nil {
				t.Error(e)
			}
		})
	}
	if e != nil || h == nil {
		t.Fatalf("original Start: %v", e)
	}
	return h
}
func transportWait(t *testing.T, ctx context.Context, h *process.Handle) {
	t.Helper()
	if e := h.Wait(ctx); e != nil {
		t.Fatal(e)
	}
	if h.ExitCode() != 0 {
		t.Fatalf("exit=%d output=%s", h.ExitCode(), h.Output())
	}
	if e := h.ConfirmTreeExited(); e != nil {
		t.Fatal(e)
	}
	t.Logf("original pid=%d spawned=%s wait=true exit=0 tree=true", h.PID(), h.OriginalSpawnedAt().Format(time.RFC3339Nano))
}
func shellFilterQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func TestCarrierTransportRealGitRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	// The fixture's helper pin is explicitly supplied comparison data. This is
	// not production ownerExecution authorization or a durable Host admission.
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	module, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	goPath := filepath.Join(runtime.GOROOT(), "bin/go")
	goSHA, err := process.ExecutableDigest(ctx, goPath)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "orchestrator")
	build := transportOriginalCommand(t, ctx, process.Command{Path: goPath, PinnedPath: goPath, PinnedSHA256: goSHA, Args: []string{"build", "-trimpath", "-buildvcs=false", "-o", helper, "./cmd/orchestrator"}, Dir: module, ExactEnv: true, Env: os.Environ()})
	transportWait(t, ctx, build)
	helperSHA, err := process.ExecutableDigest(ctx, helper)
	if err != nil {
		t.Fatal(err)
	}
	git, err := filepath.EvalSymlinks("/usr/bin/git")
	if err != nil {
		t.Fatal(err)
	}
	gitSHA, err := process.ExecutableDigest(ctx, git)
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	if err = os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + root, "TMPDIR=/private/tmp", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	init := transportOriginalCommand(t, ctx, process.Command{Path: git, PinnedPath: git, PinnedSHA256: gitSHA, Args: []string{"init", "-q", repo}, Dir: root, ExactEnv: true, Env: env})
	transportWait(t, ctx, init)
	if err = os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("file.txt filter=fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	actorRoot, err := os.MkdirTemp(filepath.Join(module, "../../tasks/filter-carrier-transport/tmp"), "broker-")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{root: actorRoot}
	t.Cleanup(func() {
		if !f.retain {
			_ = filepath.WalkDir(actorRoot, func(p string, d os.DirEntry, e error) error {
				if e == nil && d.IsDir() {
					_ = os.Chmod(p, 0700)
				}
				return e
			})
			if e := os.RemoveAll(actorRoot); e != nil {
				t.Error(e)
			}
		} else {
			t.Logf("retained_unknown_broker=%s", actorRoot)
		}
	})
	actor := filepath.Join(actorRoot, "filter")
	ab := transportOriginalCommand(t, ctx, process.Command{Path: goPath, PinnedPath: goPath, PinnedSHA256: goSHA, Args: []string{"build", "-trimpath", "-buildvcs=false", "-o", actor, "./internal/gitfilter/testdata/actor"}, Dir: module, ExactEnv: true, Env: os.Environ()})
	transportWait(t, ctx, ab)
	actorSHA, e := process.ExecutableDigest(ctx, actor)
	if e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(actor)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(actor, 0500); e != nil {
		t.Fatal(e)
	}
	f.tool = Tool{actor, actorSHA, info.Size()}
	req := brokerRequest(t, f, "xor")
	b := liveBroker(t, ctx, f, req)
	canonical := []byte("canonical\x00content\n")
	rendered := append([]byte(nil), canonical...)
	for i := range rendered {
		rendered[i] ^= 0x5a
	}
	run := func(op string, input, expected []byte, args []string) []byte {
		j := &exactTestJournal{}
		session, e := OpenExactTransformSession(b, b.Binding(), req.Manifest, []ExactTransformPermit{{Path: "file.txt", Operation: op, InputSHA256: exactHash(input), OutputSHA256: exactHash(expected), InputBytes: int64(len(input)), OutputBytes: int64(len(expected)), MaximumCalls: 1}}, j)
		if e != nil {
			t.Fatal(e)
		}
		g, e := NewOriginalGitGate(ctx, session, exactHash([]byte(op)))
		if e != nil {
			t.Fatal(e)
		}
		binding := carrierTestBinding()
		binding.Original = b.Binding()
		o, e := newCarrierSocket(ctx, req.SegmentDirectory, binding, &carrierSocketTestJournal{})
		if e != nil {
			if o != nil {
				_ = o.release(ctx)
			}
			t.Fatal(e)
		}
		tr, e := newCarrierTransport(ctx, o, g)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = tr.drain(ctx); _ = o.release(ctx) })
		route, e := tr.helperRoute(ctx, CarrierHelperExecutableExpectation{helper, helperSHA})
		if e != nil {
			t.Fatal(e)
		}
		if e = helperNamed(route); e != nil {
			t.Fatalf("positive real Git named control: %v", e)
		}
		routing, e := EncodeCarrierHelperRoute(route)
		if e != nil {
			t.Fatal(e)
		}
		command := shellFilterQuote(helper) + " filter-carrier-helper --routing " + routing
		gitArgs := append([]string{"-c", "filter.fixture.process=" + command, "-c", "filter.fixture.required=true"}, args...)
		var output bytes.Buffer
		done := make(chan error, 1)
		go func() { done <- tr.serve() }()
		h := transportOriginalCommand(t, ctx, process.Command{Path: git, PinnedPath: git, PinnedSHA256: gitSHA, Args: gitArgs, Dir: repo, Env: env, ExactEnv: true, Stdin: input, Stdout: &output})
		// Synthetic fixture ordering; production durable spawned/postguard remains
		// a required future Host contract, never inferred from this test.
		if e = tr.activate(ctx, h, g.digest); e != nil {
			t.Fatal(e)
		}
		transportWait(t, ctx, h)
		if e = tr.drain(ctx); e != nil {
			t.Fatalf("drain: %v gate=%+v", e, g.State())
		}
		if e = gateResult(t, done); e != nil {
			t.Fatalf("serve: %v", e)
		}
		if e = tr.removeAfterOriginalGit(ctx, h); e != nil {
			t.Fatal(e)
		}
		if e = session.Close(); e != nil {
			t.Fatal(e)
		}
		if g.State().Calls != 1 || g.State().ResponsesCommitted != 1 || len(j.records) != 2 || !j.records[1].WaitProved || !j.records[1].TreeProved || j.records[1].Unknown {
			t.Fatalf("missing original terminals gate=%+v journal=%+v", g.State(), j.records)
		}
		if _, e = os.Lstat(o.state.directory); !os.IsNotExist(e) {
			t.Fatal("carrier not removed")
		}
		t.Logf("real_git operation=%s calls=1 original_tool_wait/tree=true carrier_removed=true", op)
		return append([]byte(nil), output.Bytes()...)
	}
	oid := strings.TrimSpace(string(run("clean", rendered, canonical, []string{"hash-object", "-w", "--path=file.txt", "--stdin"})))
	plain := transportOriginalCommand(t, ctx, process.Command{Path: git, PinnedPath: git, PinnedSHA256: gitSHA, Args: []string{"cat-file", "blob", oid}, Dir: repo, Env: env, ExactEnv: true})
	transportWait(t, ctx, plain)
	if !bytes.Equal([]byte(plain.Output()), canonical) {
		t.Fatalf("canonical blob=%x", plain.Output())
	}
	back := run("smudge", canonical, rendered, []string{"cat-file", "--filters", "--path=file.txt", oid})
	if !bytes.Equal(back, rendered) {
		t.Fatalf("roundtrip=%x want=%x", back, rendered)
	}
	receipt, err := b.Cleanup(ctx)
	if err != nil || receipt == nil {
		f.retain = true
		t.Fatalf("original Broker cleanup: %v", err)
	}
	t.Logf("real_git_reverse_roundtrip=true original_broker_cleanup=%d", receipt.RemovedBytes)
}

func TestCarrierHelperCancellationInterruptsAuthenticatedPipes(t *testing.T) {
	tr, owner := transportFixture(t)
	path, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	path, e = filepath.EvalSymlinks(path)
	if e != nil {
		t.Fatal(e)
	}
	sha, e := process.ExecutableDigest(owner, path)
	if e != nil {
		t.Fatal(e)
	}
	route, e := tr.helperRoute(owner, CarrierHelperExecutableExpectation{path, sha})
	if e != nil {
		t.Fatal(e)
	}
	if e = tr.gate.activateWith(owner, gateTestOwner{peer: gateTestPeer{}}, tr.gate.digest); e != nil {
		t.Fatal(e)
	}
	in, writer, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer in.Close()
	defer writer.Close()
	reader, out, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer reader.Close()
	defer out.Close()
	server := make(chan error, 1)
	go func() { server <- tr.serve() }()
	ctx, cancel := context.WithCancel(owner)
	defer cancel()
	helper := make(chan error, 1)
	go func() { helper <- RunCarrierHelper(ctx, route, in, out) }()
	if _, e = writer.Write([]byte(fpGreeting())); e != nil {
		t.Fatal(e)
	}
	if e = reader.SetReadDeadline(time.Now().Add(time.Second)); e != nil {
		t.Fatal(e)
	}
	header := make([]byte, 4)
	if _, e = io.ReadFull(reader, header); e != nil {
		t.Fatalf("authenticated greeting response: %v", e)
	}
	cancel()
	select {
	case e = <-helper:
		if e == nil {
			t.Fatal("canceled helper succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("authenticated helper pipe read did not interrupt")
	}
	if e = tr.drain(owner); e != nil {
		t.Fatal(e)
	}
	_ = gateResult(t, server)
	if tr.gate.State().Calls != 0 {
		t.Fatal("cancellation reached Exchange")
	}
	t.Log("authenticated_greeting=true blocked_stdin_and_socket_reads_interrupted=true")
}
