//go:build darwin

package gitfilter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type fixture struct {
	root, empty, external string
	tool                  Tool
	retain                bool
}

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func newFixture(t *testing.T) *fixture {
	t.Helper()
	cwd, e := os.Getwd()
	if e != nil {
		t.Fatal(e)
	}
	base := filepath.Clean(filepath.Join(cwd, "../../../../tasks/filter-stream-broker/tmp"))
	root, e := os.MkdirTemp(base, "executor-")
	if e != nil {
		t.Fatal(e)
	}
	f := &fixture{root: root}
	t.Cleanup(func() {
		if f.retain {
			t.Logf("retained_unknown_root=%s", root)
			return
		}
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
			if e == nil && d.IsDir() {
				_ = os.Chmod(p, 0700)
			}
			return e
		})
		if e := os.RemoveAll(root); e != nil {
			t.Errorf("cleanup: %v", e)
		}
	})
	for _, name := range []string{"tools", "empty", "home", "cache", "gopath"} {
		if e = os.Mkdir(filepath.Join(root, name), 0700); e != nil {
			t.Fatal(e)
		}
	}
	f.empty = filepath.Join(root, "empty")
	f.external = filepath.Join(root, "sentinel")
	if e = os.WriteFile(f.external, []byte("sentinel"), 0600); e != nil {
		t.Fatal(e)
	}
	goPath := filepath.Join(runtime.GOROOT(), "bin/go")
	goPath, e = filepath.EvalSymlinks(goPath)
	if e != nil {
		t.Fatal(e)
	}
	pin, e := process.ExecutableDigest(context.Background(), goPath)
	if e != nil {
		t.Fatal(e)
	}
	actor := filepath.Join(root, "tools/filter")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f.retain = true
	h, e := process.Start(ctx, process.Command{Path: goPath, PinnedPath: goPath, PinnedSHA256: pin, Args: []string{"build", "-trimpath", "-buildvcs=false", "-o", actor, "./testdata/actor"}, Dir: cwd, ExactEnv: true, Env: []string{"PATH=" + filepath.Dir(goPath) + ":/usr/bin:/bin", "HOME=" + filepath.Join(root, "home"), "GOCACHE=" + filepath.Join(root, "cache"), "GOPATH=" + filepath.Join(root, "gopath"), "GOENV=off", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "TMPDIR=" + root}})
	if h == nil {
		f.retain = false
		t.Fatalf("build start: %v", e)
	}
	w := h.Wait(ctx)
	if w != nil || e != nil {
		clean, c := context.WithTimeout(context.Background(), 10*time.Second)
		_ = h.Stop(clean)
		w = h.Wait(clean)
		c()
	}
	tree := h.ConfirmTreeExited()
	f.retain = w != nil || tree != nil
	t.Logf("build_original_wait=%v exit=%d tree=%v root=%s", w, h.ExitCode(), tree, root)
	if e != nil || w != nil || tree != nil || h.ExitCode() != 0 {
		t.Fatalf("build: %v %s", e, h.Output())
	}
	info, e := os.Stat(actor)
	if e != nil {
		t.Fatal(e)
	}
	sha, e := process.ExecutableDigest(context.Background(), actor)
	if e != nil {
		t.Fatal(e)
	}
	f.tool = Tool{actor, sha, info.Size()}
	if e = os.Chmod(actor, 0500); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(filepath.Dir(actor), 0500); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(f.empty, 0500); e != nil {
		t.Fatal(e)
	}
	t.Logf("actor_sha256=%s size=%d", sha, info.Size())
	return f
}
func (f *fixture) request(t *testing.T, mode string, input []byte) Request {
	t.Helper()
	in, e := os.CreateTemp(f.root, "input-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { in.Close() })
	if _, e = in.Write(input); e != nil {
		t.Fatal(e)
	}
	out, e := os.CreateTemp(f.root, "output-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { out.Close() })
	return Request{Operation: "clean", ScopeSHA256: strings.Repeat("a", 64), Tool: f.tool, Args: []string{mode}, Input: in, Output: out, EmptyDirectory: f.empty}
}
func (f *fixture) run(t *testing.T, r Request, ctx context.Context) Result {
	t.Helper()
	got, e := Execute(ctx, r)
	if got.Unknown || got.Started && (!got.WaitProved || !got.TreeProved) {
		f.retain = true
	}
	t.Logf("mode=%s accepted=%t started=%t original_wait=%t exit=%d tree=%t unknown=%t error=%v", r.Args[0], got.Accepted, got.Started, got.WaitProved, got.ExitCode, got.TreeProved, got.Unknown, e)
	return got
}
func read(t *testing.T, f *os.File) []byte {
	t.Helper()
	s, e := f.Stat()
	if e != nil {
		t.Fatal(e)
	}
	b := make([]byte, s.Size())
	if _, e = f.ReadAt(b, 0); e != nil && e != io.EOF {
		t.Fatal(e)
	}
	return b
}
func TestStreamExecutorOriginalProcessAndBounds(t *testing.T) {
	f := newFixture(t)
	ctx, c := context.WithTimeout(context.Background(), 60*time.Second)
	defer c()
	t.Run("round_trip", func(t *testing.T) {
		input := []byte("canonical\x00bytes\n")
		r := f.request(t, "xor", input)
		one := f.run(t, r, ctx)
		if !one.Accepted || one.InputSHA256 != digest(input) || one.OutputSHA256 != digest(read(t, r.Output)) {
			t.Fatal("smudge not accepted/bound")
		}
		view := read(t, r.Output)
		if bytes.Equal(view, input) {
			t.Fatal("not transformed")
		}
		r = f.request(t, "xor", view)
		r.Operation = "smudge"
		two := f.run(t, r, ctx)
		if !two.Accepted || !bytes.Equal(read(t, r.Output), input) {
			t.Fatal("canonical bytes not restored")
		}
	})
	t.Run("exact_limit", func(t *testing.T) {
		input := bytes.Repeat([]byte("x"), 16<<20)
		r := f.request(t, "copy", input)
		g := f.run(t, r, ctx)
		if !g.Accepted || g.OutputBytes != 16<<20 || g.OutputSHA256 != digest(input) {
			t.Fatal("exact limit rejected")
		}
	})
	t.Run("input_over_limit", func(t *testing.T) {
		r := f.request(t, "copy", make([]byte, (16<<20)+1))
		g := f.run(t, r, ctx)
		if g.Started || g.Accepted {
			t.Fatal("oversize input executed")
		}
	})
	t.Run("output_over_limit", func(t *testing.T) {
		r := f.request(t, "overflow", nil)
		g := f.run(t, r, ctx)
		if g.Accepted || !g.Started || !g.WaitProved || !g.TreeProved {
			t.Fatal("overflow lacks original terminal")
		}
		if len(read(t, r.Output)) > 16<<20 {
			t.Fatal("output exceeded cap")
		}
	})
	t.Run("nonzero_is_known", func(t *testing.T) {
		r := f.request(t, "fail", nil)
		g := f.run(t, r, ctx)
		if g.Accepted || g.Unknown || !g.WaitProved || !g.TreeProved || g.ExitCode != 7 {
			t.Fatal("nonzero exit confused with success/UNKNOWN")
		}
	})
	t.Run("cancel_original_wait", func(t *testing.T) {
		r := f.request(t, "hang", nil)
		cancelCtx, stop := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer stop()
		g := f.run(t, r, cancelCtx)
		if g.Accepted || g.Unknown || !g.Started || !g.WaitProved || !g.TreeProved {
			t.Fatal("cancel lost original wait/tree")
		}
	})
	t.Run("aliases_rejected", func(t *testing.T) {
		r := f.request(t, "copy", nil)
		same, e := os.OpenFile(r.Input.Name(), os.O_RDWR, 0600)
		if e != nil {
			t.Fatal(e)
		}
		defer same.Close()
		r.Output = same
		g := f.run(t, r, ctx)
		if g.Started || g.Accepted {
			t.Fatal("same inode executed")
		}
	})
	t.Run("writable_tool_rejected", func(t *testing.T) {
		r := f.request(t, "copy", nil)
		if e := os.Chmod(f.tool.Path, 0700); e != nil {
			t.Fatal(e)
		}
		defer os.Chmod(f.tool.Path, 0500)
		g := f.run(t, r, ctx)
		if g.Started || g.Accepted {
			t.Fatal("writable tool executed")
		}
	})
	t.Run("pin_and_scope_shape", func(t *testing.T) {
		r := f.request(t, "copy", nil)
		r.Tool.SHA256 = strings.Repeat("b", 64)
		if g := f.run(t, r, ctx); g.Started || g.Accepted {
			t.Fatal("changed pin executed")
		}
		r = f.request(t, "copy", nil)
		r.ScopeSHA256 = "not-a-digest"
		if g := f.run(t, r, ctx); g.Started || g.Accepted {
			t.Fatal("bad scope executed")
		}
	})
}
func TestStreamExecutorSnapshotAndStartupCleanup(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t.Run("snapshot", func(t *testing.T) {
		r := f.request(t, "copy", []byte("snapshot"))
		originalScope := r.ScopeSHA256
		got, e := execute(ctx, r, func(c context.Context, cmd process.Command) (*process.Handle, io.ReadWriteCloser, error) {
			r.Args[0] = "fail"
			r.ScopeSHA256 = strings.Repeat("b", 64)
			r.Tool.SHA256 = strings.Repeat("c", 64)
			return process.StartDuplex(c, cmd)
		})
		if got.Unknown || got.Started && (!got.WaitProved || !got.TreeProved) {
			f.retain = true
		}
		if e != nil || !got.Accepted || got.ScopeSHA256 != originalScope || !bytes.Equal(read(t, r.Output), []byte("snapshot")) {
			t.Fatalf("entry snapshot drifted: %+v %v", got, e)
		}
	})
	t.Run("nonnull_handle_nil_stream", func(t *testing.T) {
		r := f.request(t, "hang", nil)
		got, e := execute(ctx, r, func(c context.Context, cmd process.Command) (*process.Handle, io.ReadWriteCloser, error) {
			h, stream, e := process.StartDuplex(c, cmd)
			if e != nil {
				return h, stream, e
			}
			_ = stream.Close()
			return h, nil, errors.New("controlled_after_start_failure")
		})
		if got.Unknown || got.Started && (!got.WaitProved || !got.TreeProved) {
			f.retain = true
		}
		if e == nil || got.Accepted || !got.Started || !got.WaitProved || !got.TreeProved {
			t.Fatalf("failed Start lost original cleanup: %+v %v", got, e)
		}
		t.Logf("failed_start_original_wait=%t tree=%t exit=%d", got.WaitProved, got.TreeProved, got.ExitCode)
	})
	t.Run("append_and_readonly_output", func(t *testing.T) {
		for _, flags := range []int{os.O_RDWR | os.O_APPEND, os.O_RDONLY} {
			r := f.request(t, "copy", nil)
			fd, e := os.OpenFile(r.Output.Name(), flags, 0600)
			if e != nil {
				t.Fatal(e)
			}
			defer fd.Close()
			r.Output = fd
			got := f.run(t, r, ctx)
			if got.Started || got.Accepted {
				t.Fatal("unsafe output descriptor executed")
			}
		}
	})
	t.Run("directory_drift", func(t *testing.T) {
		r := f.request(t, "copy", []byte("test"))
		got, e := execute(ctx, r, func(c context.Context, cmd process.Command) (*process.Handle, io.ReadWriteCloser, error) {
			h, stream, e := process.StartDuplex(c, cmd)
			if e != nil {
				return h, stream, e
			}
			if e = os.Chmod(r.EmptyDirectory, 0700); e != nil {
				return h, stream, e
			}
			return h, stream, e
		})
		if got.Unknown || got.Started && (!got.WaitProved || !got.TreeProved) {
			f.retain = true
		}
		if e == nil || got.Accepted || !got.WaitProved || !got.TreeProved {
			t.Fatalf("directory drift accepted: %+v %v", got, e)
		}
		if e = os.Chmod(r.EmptyDirectory, 0500); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("input_drift", func(t *testing.T) {
		r := f.request(t, "copy", []byte("original"))
		got, e := execute(ctx, r, func(c context.Context, cmd process.Command) (*process.Handle, io.ReadWriteCloser, error) {
			if _, e := r.Input.WriteAt([]byte("changed!"), 0); e != nil {
				return nil, nil, e
			}
			return process.StartDuplex(c, cmd)
		})
		if got.Unknown || got.Started && (!got.WaitProved || !got.TreeProved) {
			f.retain = true
		}
		if e == nil || got.Accepted || !got.WaitProved || !got.TreeProved {
			t.Fatalf("changed input accepted: %+v %v", got, e)
		}
	})

}

func TestStreamExecutorKernelIsolationWithControl(t *testing.T) {
	f := newFixture(t)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	ctx, c := context.WithTimeout(context.Background(), 30*time.Second)
	defer c()
	// Each negative has a real unsandboxed control. In-place exec is a separate
	// control because success replaces the actor and emits an external marker.
	control := func(args []string) string {
		t.Helper()
		f.retain = true
		h, e := process.Start(ctx, process.Command{Path: f.tool.Path, PinnedPath: f.tool.Path, PinnedSHA256: f.tool.SHA256, Args: args, Dir: f.empty, ExactEnv: true, Env: []string{"HOME=" + f.empty, "PATH=/usr/bin:/bin"}})
		if h == nil {
			f.retain = false
			t.Fatal(e)
		}
		w := h.Wait(ctx)
		if w != nil {
			clean, c := context.WithTimeout(context.Background(), 10*time.Second)
			_ = h.Stop(clean)
			w = h.Wait(clean)
			c()
		}
		tree := h.ConfirmTreeExited()
		f.retain = w != nil || tree != nil
		t.Logf("control_original_wait=%v exit=%d tree=%v", w, h.ExitCode(), tree)
		if e != nil || w != nil || tree != nil || h.ExitCode() != 0 {
			t.Fatal("control failed")
		}
		return h.Output()
	}
	args := []string{"audit", f.external, listener.Addr().String(), "reserved"}
	out := control(args)
	var positive map[string]string
	if e = json.Unmarshal([]byte(out), &positive); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"external_read", "external_write", "empty_chmod", "empty_write", "self_chmod", "fork", "network"} {
		if positive[name] != "allowed" {
			t.Fatalf("control %s = %s", name, positive[name])
		}
	}
	if out = control([]string{"exec_other"}); !strings.Contains(out, "exec-allowed") {
		t.Fatalf("in-place exec control: %s", out)
	}

	// Restore control writes only after original Wait/tree, before isolated call.
	if e = os.Remove(filepath.Join(f.empty, "entry")); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(f.external, []byte("sentinel"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(f.empty, 0500); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(f.tool.Path, 0500); e != nil {
		t.Fatal(e)
	}
	r := f.request(t, "audit", nil)
	r.Args = args
	g := f.run(t, r, ctx)
	if !g.Accepted {
		t.Fatal("isolated audit did not finish")
	}
	var checks map[string]string
	if e = json.Unmarshal(read(t, r.Output), &checks); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"external_read", "external_write", "empty_chmod", "self_chmod", "fork", "network"} {
		if checks[name] != "denied" {
			t.Errorf("%s = %s", name, checks[name])
		}
	}
	r = f.request(t, "exec_other", nil)
	g = f.run(t, r, ctx)
	if !g.Accepted || !bytes.Equal(read(t, r.Output), []byte("exec-denied\n")) {
		t.Fatal("in-place exec escaped")
	}
	contents, e := os.ReadFile(f.external)
	if e != nil || string(contents) != "sentinel" {
		t.Fatal("sentinel changed")
	}
	entries, e := os.ReadDir(f.empty)
	if e != nil || len(entries) != 0 {
		t.Fatal("empty directory changed")
	}
}
