//go:build darwin

package gitops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type filterProbeOutcome struct {
	Allowed bool   `json:"allowed"`
	Denied  bool   `json:"denied"`
	Error   string `json:"error,omitempty"`
}

// This is an OS feasibility experiment, not a public candidate-filter test.
// Production still rejects filter configuration. The compiled actor and all
// paths, listeners and executables below belong exclusively to this test.
func TestFilterSandboxDarwinFeasibility(t *testing.T) {
	if os.Getenv("FILTER_SANDBOX_PROBE") != "1" {
		t.Skip("explicit owned native feasibility probe")
	}
	root, err := os.MkdirTemp("/private/tmp", "filter-sandbox-")
	if err != nil {
		t.Fatal(err)
	}
	safeCleanup := true
	t.Cleanup(func() {
		if safeCleanup {
			if err := os.RemoveAll(root); err != nil {
				t.Errorf("owned fixture cleanup: %v", err)
			}
		} else {
			t.Logf("retained uncertain fixture=%s", root)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	actor := filepath.Join(root, "filter-actor")
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	goPath, err = filepath.EvalSymlinks(goPath)
	if err != nil || !filepath.IsAbs(goPath) || runtime.Version() != "go1.26.8" {
		t.Fatalf("toolchain scope: path=%s version=%s err=%v", goPath, runtime.Version(), err)
	}
	goSHA, err := process.ExecutableDigest(ctx, goPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"build-home", "build-cache", "build-mod", "build-go", "build-tmp"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	buildEnv := []string{"PATH=" + filepath.Dir(goPath) + ":/usr/bin:/bin:/usr/sbin:/sbin", "GOENV=off", "GOWORK=off", "GOFLAGS=", "GOEXPERIMENT=", "GOOS=darwin", "GOARCH=arm64", "GOARM64=v8.0", "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "HOME=" + filepath.Join(root, "build-home"), "GOCACHE=" + filepath.Join(root, "build-cache"), "GOMODCACHE=" + filepath.Join(root, "build-mod"), "GOPATH=" + filepath.Join(root, "build-go"), "TMPDIR=" + filepath.Join(root, "build-tmp")}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	build, err := process.Start(ctx, process.Command{Path: goPath, PinnedPath: goPath, PinnedSHA256: goSHA, Args: []string{"build", "-trimpath", "-buildvcs=false", "-o", actor, "./testdata/filter-sandbox-probe"}, Dir: cwd, Env: buildEnv, ExactEnv: true})
	if err != nil {
		if build != nil && process.CleanupFailedStart(context.Background(), build, 10*time.Second) != nil {
			safeCleanup = false
		}
		t.Fatal(err)
	}
	buildWait := build.Wait(ctx)
	if buildWait != nil {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		if build.Stop(cleanupCtx) != nil {
			safeCleanup = false
		}
		stop()
	}
	buildTree := build.ConfirmTreeExited()
	if buildTree != nil {
		safeCleanup = false
	}
	t.Logf("build_go_path=%s sha256=%s test_toolchain=%s original_wait=%v exit=%d tree=%v", goPath, goSHA, runtime.Version(), buildWait, build.ExitCode(), buildTree)
	if buildWait != nil || buildTree != nil || build.ExitCode() != 0 {
		t.Fatalf("actor build: %s", build.Output())
	}
	actorSHA, err := process.ExecutableDigest(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	sandboxSHA, err := process.ExecutableDigest(ctx, "/usr/bin/sandbox-exec")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("compiled_actor_sha256=%s sandbox_exec_sha256=%s build_original_wait=0", actorSHA, sandboxSHA)
	var protected []string
	const sentinel = "original-owned-sentinel\n"
	for _, name := range []string{"worktree", "common.git", "host-state", "external"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "protected")
		if err := os.WriteFile(path, []byte(sentinel), 0600); err != nil {
			t.Fatal(err)
		}
		protected = append(protected, path)
	}
	tcp, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	unix, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(root, "u.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close()
	checkConnections := func(t *testing.T, want bool) {
		t.Helper()
		for _, listener := range []interface {
			SetDeadline(time.Time) error
			Accept() (net.Conn, error)
		}{tcp, unix} {
			if err := listener.SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			conn, err := listener.Accept()
			if conn != nil {
				_ = conn.Close()
			}
			if want && err != nil {
				t.Fatalf("positive control listener: %v", err)
			}
			if !want {
				var timeout net.Error
				if conn != nil || !errors.As(err, &timeout) || !timeout.Timeout() {
					t.Fatalf("sandbox network escaped: conn=%v err=%v", conn != nil, err)
				}
			}
		}
	}
	allChecks := []string{"worktree_read", "worktree_write", "worktree_stat", "worktree_list", "common_git_read", "common_git_write", "common_git_stat", "common_git_list", "host_state_read", "host_state_write", "host_state_stat", "host_state_list", "external_read", "external_write", "external_stat", "external_list", "scratch_read", "scratch_write", "hardlink", "scratch_symlink", "symlink_read", "symlink_write", "tcp_connect", "unix_connect", "fork_exec"}
	run := func(t *testing.T, label string, isolated bool, input []byte) []byte {
		t.Helper()
		scratch := filepath.Join(root, label)
		if err := os.Mkdir(scratch, 0700); err != nil {
			t.Fatal(err)
		}
		args := append([]string{scratch, tcp.Addr().String(), unix.Addr().String()}, protected...)
		var stdout bytes.Buffer
		command := process.Command{Path: actor, PinnedPath: actor, PinnedSHA256: actorSHA, Args: args, Dir: scratch, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + scratch, "TMPDIR=" + scratch, "XDG_CACHE_HOME=" + scratch}, ExactEnv: true, Stdin: input, Stdout: &stdout}
		if isolated {
			rules := []string{"(version 1)", "(allow default)", "(deny file-read*)", "(deny file-write*)", "(deny file-link)", "(deny network*)", "(deny process-fork)", `(allow file-read* (subpath "/System") (subpath "/usr/lib"))`, `(allow file-read* (subpath "/private/preboot/Cryptexes/OS"))`, `(allow file-read-metadata)`, `(allow file-read* file-write* (literal "/dev/null"))`, `(allow file-read* (literal "/dev/random") (literal "/dev/urandom"))`, "(allow file-read* (literal " + strconv.Quote(actor) + "))", "(allow file-read* file-write* (subpath " + strconv.Quote(scratch) + "))"}
			policy := strings.Join(rules, "\n")
			t.Logf("label=%s profile=%s", label, policy)
			command.Path = "/usr/bin/sandbox-exec"
			command.Args = append([]string{"-p", policy, actor}, args...)
		}
		launchCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		defer stop()
		p, err := process.Start(launchCtx, command)
		if err != nil {
			if p != nil {
				if cleanupErr := process.CleanupFailedStart(context.Background(), p, 5*time.Second); cleanupErr != nil {
					safeCleanup = false
					t.Logf("failed start original cleanup=%v", cleanupErr)
				}
			}
			t.Fatalf("start: %v", err)
		}
		waitErr := p.Wait(launchCtx)
		if waitErr != nil && p.ExitCode() == -1 {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			cleanupErr := p.Stop(cleanupCtx)
			cleanupCancel()
			if cleanupErr != nil {
				safeCleanup = false
				t.Logf("original stop: %v", cleanupErr)
			}
		}
		treeErr := p.ConfirmTreeExited()
		if treeErr != nil {
			safeCleanup = false
		}
		identity, _ := json.Marshal(p.Identity())
		t.Logf("label=%s original_identity=%s original_wait=%v exit=%d tree=%v", label, identity, waitErr, p.ExitCode(), treeErr)
		if waitErr != nil || p.ExitCode() != 0 || treeErr != nil {
			t.Fatalf("actor: wait=%v tree=%v output=%s", waitErr, treeErr, p.Output())
		}
		raw, err := os.ReadFile(filepath.Join(scratch, "checks.json"))
		var checks map[string]filterProbeOutcome
		if err != nil || json.Unmarshal(raw, &checks) != nil || len(checks) != len(allChecks) {
			t.Fatalf("actor checks: %s %v", raw, err)
		}
		t.Logf("label=%s checks=%s", label, raw)
		for _, name := range allChecks {
			o, present := checks[name]
			wantAllowed := !isolated || name == "scratch_read" || name == "scratch_write" || name == "scratch_symlink" || strings.HasSuffix(name, "_stat")
			if !present || o.Allowed != wantAllowed || !wantAllowed && !o.Denied {
				t.Errorf("%s kernel permission mismatch: %+v wantAllowed=%v", name, o, wantAllowed)
			}
		}
		checkConnections(t, !isolated)
		for _, path := range protected {
			if isolated {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != sentinel {
					t.Errorf("protected sentinel changed: %s err=%v", path, err)
				}
			} else if err := os.WriteFile(path, []byte(sentinel), 0600); err != nil {
				t.Fatal(err)
			}
		}
		for path, expected := range map[string]string{actor: actorSHA, "/usr/bin/sandbox-exec": sandboxSHA} {
			got, err := process.ExecutableDigest(ctx, path)
			if err != nil || got != expected {
				t.Errorf("executable drift: %s %v", path, err)
			}
		}
		output := append([]byte(nil), stdout.Bytes()...)
		if len(output) != len(input) {
			t.Fatalf("transform length %d != %d", len(output), len(input))
		}
		return output
	}
	input := []byte("canonical Git bytes\x00\xff\r\nwith binary content\n")
	if !t.Run("positive_control", func(t *testing.T) {
		got := run(t, "control", false, input)
		if bytes.Equal(got, input) {
			t.Fatal("actor did not transform stdin")
		}
	}) {
		return
	}
	t.Run("isolated_round_trip", func(t *testing.T) {
		smudge := run(t, "smudge", true, input)
		clean := run(t, "clean", true, smudge)
		if !reflect.DeepEqual(clean, input) || bytes.Equal(smudge, input) {
			t.Fatal("stdio round-trip changed canonical bytes")
		}
		canonicalSHA, viewSHA := sha256.Sum256(input), sha256.Sum256(smudge)
		t.Logf("canonical_sha256=%s smudge_sha256=%s exact_round_trip=true", hex.EncodeToString(canonicalSHA[:]), hex.EncodeToString(viewSHA[:]))
	})
}
