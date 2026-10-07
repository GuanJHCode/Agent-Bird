//go:build darwin

package grokorigin

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type originPolicyProbe struct{ Operation, Path string }
type originPolicyResult struct {
	Allowed bool
	Error   string
}

func TestOriginSandboxPolicyActor(t *testing.T) {
	if os.Getenv("AGENT_BIRD_ORIGIN_POLICY_ACTOR") != "1" {
		t.Skip("owned native policy actor")
	}
	_, _ = os.Stdout.WriteString("ready\n")
	d := json.NewDecoder(os.Stdin)
	e := json.NewEncoder(os.Stdout)
	for {
		var req originPolicyProbe
		if err := d.Decode(&req); err == io.EOF {
			os.Exit(0)
		} else if err != nil {
			os.Exit(2)
		}
		var err error
		switch req.Operation {
		case "read":
			_, err = os.ReadFile(req.Path)
		case "write":
			err = os.WriteFile(req.Path, []byte("owned probe"), 0600)
		case "fork":
			err = exec.Command("/usr/bin/true").Run()
		case "network":
			var conn net.Conn
			conn, err = net.DialTimeout("tcp", req.Path, time.Second)
			if conn != nil {
				_ = conn.Close()
			}
		default:
			os.Exit(3)
		}
		result := originPolicyResult{Allowed: err == nil}
		if err != nil {
			result.Error = err.Error()
		}
		if e.Encode(result) != nil {
			os.Exit(4)
		}
	}
}

// Native design experiment: do not mistake this candidate policy for a
// completed public launcher or real Grok tool-surface qualification.
func TestOriginSandboxPolicyProbe(t *testing.T) {
	if os.Getenv("ORIGIN_NATIVE") != "1" {
		t.Skip("explicit owned native policy probe")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work, state := filepath.Join(root, "work"), filepath.Join(root, "state")
	if err = os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	layout, err := newPrivateLayout(state)
	if err != nil {
		t.Fatal(err)
	}
	var original *process.Handle
	t.Cleanup(func() {
		defer layout.closeFiles()
		if original != nil && original.ConfirmTreeExited() != nil {
			t.Error("private layout retained: original tree not proven exited")
			return
		}
		if err := layout.remove(); err != nil {
			t.Errorf("private layout cleanup: %v", err)
		}
	})
	private := layout.home
	if err = os.MkdirAll(filepath.Join(work, ".grok"), 0700); err != nil {
		t.Fatal(err)
	}
	ordinary, secret := filepath.Join(work, "ordinary.txt"), filepath.Join(state, "control-secret")
	for _, path := range []string{ordinary, secret} {
		if err = os.WriteFile(path, []byte("owned fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(work, ".grok", "config.toml")
	denied, err := inspectWorkspace(work)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := originSandboxPolicy(layout, denied, "none")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	digest, err := process.ExecutableDigest(context.Background(), executable)
	if err != nil {
		t.Fatal(err)
	}
	h, stream, err := process.StartDuplex(context.Background(), process.Command{Path: "/usr/bin/sandbox-exec", Args: []string{"-p", rules, executable, "-test.run=^TestOriginSandboxPolicyActor$"}, PinnedPath: executable, PinnedSHA256: digest, Dir: work, ExactEnv: true, Env: []string{"AGENT_BIRD_ORIGIN_POLICY_ACTOR=1"}})
	original = h
	if h != nil {
		t.Cleanup(func() {
			if stream != nil {
				_ = stream.Close()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if e := h.Stop(ctx); e != nil {
				t.Errorf("original Stop: %v", e)
			}
			if e := h.Wait(ctx); e != nil {
				t.Errorf("original Wait: %v", e)
			}
			if e := h.ConfirmTreeExited(); e != nil {
				t.Errorf("original tree: %v", e)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan string, 1)
	reader := bufio.NewReader(stream)
	go func() { line, _ := reader.ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if line != "ready\n" {
			t.Fatalf("actor ready=%q stderr=%q", line, h.Output())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("actor did not start")
	}
	actual, err := process.ExecutablePath(h.PID())
	if err != nil || actual != executable {
		t.Fatalf("wrapper did not exec pin at original PID: %q %v", actual, err)
	}
	actualBirth, err := process.Birth(h.PID())
	if err != nil || actualBirth != h.Identity().Birth {
		t.Fatal("original birth changed")
	}
	encode, decode := json.NewEncoder(stream), json.NewDecoder(reader)
	probe := func(name, operation, path string, want bool) {
		t.Helper()
		if err := encode.Encode(originPolicyProbe{operation, path}); err != nil {
			t.Fatal(err)
		}
		var result originPolicyResult
		if err := decode.Decode(&result); err != nil {
			t.Fatal(err)
		}
		if result.Allowed != want {
			t.Errorf("%s allowed=%t want=%t err=%s", name, result.Allowed, want, result.Error)
		}
	}
	probe("ordinary read", "read", ordinary, true)
	probe("state read", "read", secret, false)
	probe("workspace write", "write", filepath.Join(work, "blocked"), false)
	probe("private write", "write", filepath.Join(private, "allowed"), true)
	probe("fork", "fork", "", false)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	probe("networknone", "network", listener.Addr().String(), false)
	if err = os.WriteFile(source, []byte("late capability"), 0600); err != nil {
		t.Fatal(err)
	}
	probe("late regular capability", "read", source, false)
	if err = os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(ordinary, source); err != nil {
		t.Fatal(err)
	}
	probe("late symlink capability", "read", source, false)
	if cw, ok := stream.(interface{ CloseWrite() error }); !ok {
		t.Fatal("missing half close")
	} else if err = cw.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = h.Wait(ctx); err != nil || h.ExitCode() != 0 || h.ConfirmTreeExited() != nil {
		t.Fatalf("original exit=%d wait=%v", h.ExitCode(), err)
	}
	t.Logf("original PID=%d PGID=%d birth=%s Wait=0 tree_exited=true", h.PID(), h.PGID(), actualBirth)
}
