package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

var testBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "g0-return-lab-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	testBinary = filepath.Join(dir, "g0-return-lab")
	args := []string{"build", "-o", testBinary, "."}
	if os.Getenv("G0_LAB_TEST_RACE") == "1" {
		args = []string{"build", "-race", "-o", testBinary, "."}
	}
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func command(args ...string) *exec.Cmd {
	cmd := exec.Command(testBinary, args...)
	// Avoid the race detector's artificial exit sleep in subprocess timing tests.
	cmd.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0")
	return cmd
}

func invoke(t *testing.T, want int, args ...string) (string, string) {
	t.Helper()
	cmd := command(args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	if code != want {
		t.Fatalf("%v: exit=%d want=%d stdout=%q stderr=%q", args, code, want, out.String(), errOut.String())
	}
	return out.String(), errOut.String()
}

func privateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(s), &got); err != nil {
		t.Fatalf("invalid single JSON output %q: %v", s, err)
	}
	return got
}

func startJob(t *testing.T, dir, nonce, delay string) int {
	t.Helper()
	out, errOut := invoke(t, 0, "start", "--dir", dir, "--nonce", nonce, "--delay", delay)
	got := decode(t, out)
	if errOut != "" || len(got) != 7 || got["version"] != float64(1) || got["status"] != "started" || got["nonce"] != nonce || got["job_file"] != "job.json" || got["result_file"] != "result.json" {
		t.Fatalf("unexpected start output: %s stderr=%q", out, errOut)
	}
	created, ok := got["created_at"].(string)
	if !ok {
		t.Fatal("missing creation identity")
	}
	if _, err := time.Parse(time.RFC3339Nano, created); err != nil {
		t.Fatal(err)
	}
	pid, ok := got["worker_pid"].(float64)
	if !ok || pid <= 0 {
		t.Fatalf("missing worker PID: %s", out)
	}
	meta, err := os.ReadFile(filepath.Join(dir, "job.json"))
	if err != nil {
		t.Fatalf("start returned before metadata became readable: %v", err)
	}
	job := decode(t, string(meta))
	if job["nonce"] != nonce || job["worker_pid"] != pid || job["created_at"] != created {
		t.Fatalf("stdout and disk ownership disagree: %s / %s", out, meta)
	}
	return int(pid)
}

func assertResult(t *testing.T, out, nonce, status string, count int) {
	t.Helper()
	got := decode(t, out)
	if len(got) != 4 || got["version"] != float64(1) || got["nonce"] != nonce || got["status"] != status || got["count"] != float64(count) {
		t.Fatalf("unexpected result JSON: %s", out)
	}
}

// Catches a worker inheriting caller lifetime/stdio or reporting before metadata exists.
func TestCompletionAfterStartCallerExits(t *testing.T) {
	dir := privateDir(t)
	started := time.Now()
	pid := startJob(t, dir, "caller_exit", "2s")
	if time.Since(started) >= 1500*time.Millisecond {
		t.Fatal("start waited for the worker instead of returning promptly")
	}
	if pgid, err := syscall.Getpgid(pid); err != nil || pgid != pid {
		t.Fatalf("worker has no independent process group: pgid=%d pid=%d err=%v", pgid, pid, err)
	}
	out, _ := invoke(t, 0, "read", "--dir", dir, "--nonce", "caller_exit")
	assertResult(t, out, "caller_exit", "pending", 0)
	out, _ = invoke(t, 0, "wait", "--dir", dir, "--nonce", "caller_exit", "--timeout", "3s")
	assertResult(t, out, "caller_exit", "completed", 1)
	first, err := os.Stat(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"job.json", "result.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("%s is not private: info=%v err=%v", name, info, err)
		}
	}
	time.Sleep(150 * time.Millisecond)
	out, _ = invoke(t, 0, "read", "--dir", dir, "--nonce", "caller_exit")
	assertResult(t, out, "caller_exit", "completed", 1)
	last, _ := os.Stat(filepath.Join(dir, "result.json"))
	if !os.SameFile(first, last) || !first.ModTime().Equal(last.ModTime()) {
		t.Fatal("completed result was rewritten")
	}
	deadline := time.Now().Add(time.Second)
	for syscall.Kill(pid, 0) != syscall.ESRCH {
		if time.Now().After(deadline) {
			t.Fatal("completed worker did not exit within the observation window")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Catches timeout/cancellation being coupled to the detached execution lifetime.
func TestWaitTimeoutDoesNotStopWorker(t *testing.T) {
	dir := privateDir(t)
	startJob(t, dir, "timeout", "600ms")
	out, errOut := invoke(t, 3, "wait", "--dir", dir, "--nonce", "timeout", "--timeout", "100ms")
	if out != "" || decode(t, errOut)["error"] != "timeout" {
		t.Fatalf("ambiguous timeout: stdout=%q stderr=%q", out, errOut)
	}
	out, _ = invoke(t, 0, "wait", "--dir", dir, "--nonce", "timeout", "--timeout", "3s")
	assertResult(t, out, "timeout", "completed", 1)
}

func TestKilledWaiterDoesNotStopWorker(t *testing.T) {
	dir := privateDir(t)
	startJob(t, dir, "killed_waiter", "600ms")
	waiter := command("wait", "--dir", dir, "--nonce", "killed_waiter", "--timeout", "3s")
	if err := waiter.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := waiter.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if waiter.Wait() == nil {
		t.Fatal("waiter did not terminate")
	}
	out, _ := invoke(t, 0, "wait", "--dir", dir, "--nonce", "killed_waiter", "--timeout", "3s")
	assertResult(t, out, "killed_waiter", "completed", 1)
}

// Catches duplicate execution and cross-task reads.
func TestDuplicateStartAndNonceMismatch(t *testing.T) {
	dir := privateDir(t)
	startJob(t, dir, "original", "300ms")
	for _, nonce := range []string{"original", "different"} {
		out, _ := invoke(t, 2, "start", "--dir", dir, "--nonce", nonce, "--delay", "300ms")
		if out != "" {
			t.Fatal("failed start reported worker success")
		}
	}
	for _, sub := range []string{"read", "wait"} {
		args := []string{sub, "--dir", dir, "--nonce", "different"}
		if sub == "wait" {
			args = append(args, "--timeout", "1s")
		}
		_, errOut := invoke(t, 2, args...)
		if decode(t, errOut)["error"] != "nonce_mismatch" {
			t.Fatalf("wrong task was not identified: %s", errOut)
		}
	}
	out, _ := invoke(t, 0, "wait", "--dir", dir, "--nonce", "original", "--timeout", "3s")
	assertResult(t, out, "original", "completed", 1)
}

// Catches unbounded runs and accidental writes outside an explicit private directory.
func TestInvalidArgumentsDoNotStartWork(t *testing.T) {
	for _, delay := range []string{"0s", "99ms", "60.001s", "garbage"} {
		t.Run("delay_"+delay, func(t *testing.T) {
			dir := privateDir(t)
			_, errOut := invoke(t, 2, "start", "--dir", dir, "--nonce", "valid", "--delay", delay)
			if decode(t, errOut)["error"] != "invalid_args" {
				t.Fatalf("invalid delay not identified: %s", errOut)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Fatal("invalid delay left execution artifacts")
			}
		})
	}
	for _, nonce := range []string{"", "../escape", "space x", strings.Repeat("x", 65)} {
		invoke(t, 2, "start", "--dir", privateDir(t), "--nonce", nonce, "--delay", "1s")
	}
	for _, timeout := range []string{"0s", "99ms", "120.001s", "invalid"} {
		invoke(t, 2, "wait", "--dir", privateDir(t), "--nonce", "valid", "--timeout", timeout)
	}
	for _, args := range [][]string{
		{}, {"unknown"}, {"start", "--nonce", "x", "--delay", "1s"},
		{"read", "--dir", "relative", "--nonce", "x"},
		{"read", "--dir", privateDir(t), "--nonce", "x", "--unknown"},
		{"read", "--dir", privateDir(t), "--nonce", "x", "extra"},
	} {
		invoke(t, 2, args...)
	}
	dir := privateDir(t)
	invoke(t, 2, "start", "--dir", filepath.Join(dir, "missing"), "--nonce", "x", "--delay", "1s")
	os.Chmod(dir, 0755)
	invoke(t, 2, "start", "--dir", dir, "--nonce", "x", "--delay", "1s")
	link := filepath.Join(privateDir(t), "link")
	os.Symlink(privateDir(t), link)
	invoke(t, 2, "start", "--dir", link, "--nonce", "x", "--delay", "1s")
}

func writeFixtureExclusive(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
}

func prepareReadFixture(t *testing.T, dir string) *os.Root {
	t.Helper()
	// No worker runs in these cases: the destination exists before publish begins.
	writeFixtureExclusive(t, filepath.Join(dir, "job.json"), `{"version":1,"status":"started","nonce":"safe","worker_pid":123,"created_at":"2026-09-11T00:00:00Z","job_file":"job.json","result_file":"result.json"}`, 0600)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root
}

// Catches overwriting an already-present destination; fixtures cannot truncate a
// worker's output, and there is no timing window that can turn the check green.
func TestExistingResultIsNeverOverwrittenAndUnsafeResultsAreRejected(t *testing.T) {
	cases := []struct {
		name string
		data string
		mode os.FileMode
	}{
		{"other_nonce", `{"version":1,"status":"completed","nonce":"other","count":1}`, 0600},
		{"public_file", `{"version":1,"status":"completed","nonce":"safe","count":1}`, 0644},
		{"unknown_data", `{"version":1,"status":"completed","nonce":"safe","count":1,"secret":"never_print"}`, 0600},
		{"invalid_count", `{"version":1,"status":"completed","nonce":"safe","count":2}`, 0600},
		{"invalid_json", `{`, 0600},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := privateDir(t)
			root := prepareReadFixture(t, dir)
			path := filepath.Join(dir, "result.json")
			writeFixtureExclusive(t, path, tc.data, tc.mode)
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			publishErr := publish(root, "result.json", result{1, "completed", "safe", 1})
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) || string(got) != tc.data {
				t.Fatal("publisher overwrote an existing result")
			}
			if !errors.Is(publishErr, os.ErrExist) {
				t.Fatalf("publish did not reject the existing destination: %v", publishErr)
			}
			out, errOut := invoke(t, 2, "read", "--dir", dir, "--nonce", "safe")
			if out != "" || strings.Contains(errOut, "never_print") {
				t.Fatal("untrusted file contents leaked to output")
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		dir := privateDir(t)
		root := prepareReadFixture(t, dir)
		target := filepath.Join(privateDir(t), "foreign.json")
		data := `{"version":1,"status":"completed","nonce":"safe","count":1}`
		writeFixtureExclusive(t, target, data, 0600)
		path := filepath.Join(dir, "result.json")
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		before, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		publishErr := publish(root, "result.json", result{1, "completed", "safe", 1})
		after, err := os.Lstat(path)
		if err != nil || after.Mode()&os.ModeSymlink == 0 || !os.SameFile(before, after) {
			t.Fatal("publisher replaced an existing symlink")
		}
		got, err := os.ReadFile(target)
		if err != nil || string(got) != data {
			t.Fatal("publisher changed the symlink target")
		}
		if !errors.Is(publishErr, os.ErrExist) {
			t.Fatalf("publish did not reject the existing symlink: %v", publishErr)
		}
		invoke(t, 2, "read", "--dir", dir, "--nonce", "safe")
	})
}
