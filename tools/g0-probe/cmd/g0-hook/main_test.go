package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const privateCanary = "CANARY_PRIVATE_PROMPT_DO_NOT_RECORD"

func taskDir(t *testing.T) string {
	t.Helper()
	// Canonicalize the test harness path; the recorder itself rejects links.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func invokeHook(ctx context.Context, dir, input string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"--output-dir", dir, "--nonce", "test-nonce"}, strings.NewReader(input), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// Fixtures follow official command-hook input: session identity is common,
// source belongs to SessionStart, and UserPromptSubmit has a turn identity.
func TestAllowedEventsRecordOnlyIdentity(t *testing.T) {
	inputs := []struct {
		name, input string
		fields      map[string]any
	}{
		{"start", `{"session_id":"thr_123","hook_event_name":"SessionStart","source":"startup","cwd":"` + privateCanary + `","transcript_path":"` + privateCanary + `","model":"` + privateCanary + `"}`, map[string]any{"session_id": "thr_123", "hook_event_name": "SessionStart", "source": "startup"}},
		{"submit", `{"session_id":"thr_123","hook_event_name":"UserPromptSubmit","turn_id":"turn_456","prompt":"` + privateCanary + `","permission_mode":"default","unknown":{"raw":"` + privateCanary + `"}}`, map[string]any{"session_id": "thr_123", "hook_event_name": "UserPromptSubmit", "turn_id": "turn_456"}},
		{"end", `{"session_id":"thr_123","hook_event_name":"SessionEnd","reason":"other","cwd":"` + privateCanary + `","transcript_path":null}`, map[string]any{"session_id": "thr_123", "hook_event_name": "SessionEnd"}},
	}
	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			dir := taskDir(t)
			start := time.Now()
			code, stdout, stderr := invokeHook(context.Background(), dir, tc.input)
			if code != 0 || stdout != "" || stderr != "" {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("expected one event, count=%d error=%v", len(entries), err)
			}
			path := filepath.Join(dir, entries[0].Name())
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
				t.Fatalf("event is not a regular 0600 file: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data)+stdout+stderr, privateCanary) {
				t.Fatal("private canary leaked")
			}
			var got map[string]any
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			stamp, ok := got["recorded_at"].(string)
			if !ok {
				t.Fatal("missing recorder timestamp")
			}
			when, err := time.Parse(time.RFC3339Nano, stamp)
			if err != nil || when.Before(start) || when.After(time.Now()) {
				t.Fatal("timestamp is not the recorder's current time")
			}
			delete(got, "recorded_at")
			want := tc.fields
			want["nonce"], want["pid"], want["ppid"] = "test-nonce", float64(os.Getpid()), float64(os.Getppid())
			if !reflect.DeepEqual(got, want) {
				t.Errorf("recorded fields differ: %v", got)
			}
		})
	}
}

func TestRejectsUnsafeOrIncompleteInput(t *testing.T) {
	cases := []struct{ name, input, category string }{
		{"malformed", `{"prompt":"` + privateCanary, "invalid_input"},
		{"multiple_json", `{"session_id":"thr_123","hook_event_name":"SessionEnd"}{}`, "invalid_input"},
		{"not_object", `[]`, "invalid_input"},
		{"too_large", `{"unknown":"` + strings.Repeat("x", 65536) + `"}`, "input_limit"},
		{"missing_session", `{"hook_event_name":"SessionEnd"}`, "invalid_input"},
		{"missing_event", `{"session_id":"thr_123"}`, "invalid_input"},
		{"missing_turn", `{"session_id":"thr_123","hook_event_name":"UserPromptSubmit"}`, "invalid_input"},
		{"wrong_type", `{"session_id":123,"hook_event_name":"SessionEnd"}`, "invalid_input"},
		{"unsafe_id", `{"session_id":"thr_123\nsecret","hook_event_name":"SessionEnd"}`, "invalid_input"},
		{"long_id", `{"session_id":"` + strings.Repeat("x", 129) + `","hook_event_name":"SessionEnd"}`, "invalid_input"},
		{"duplicate", `{"session_id":"wrong","session_id":"thr_123","hook_event_name":"SessionEnd"}`, "invalid_input"},
		{"unknown_source", `{"session_id":"thr_123","hook_event_name":"SessionStart","source":"` + privateCanary + `"}`, "invalid_input"},
		{"stop_not_supported", `{"session_id":"thr_123","hook_event_name":"Stop","turn_id":"turn_456"}`, "unsupported_event"},
		{"subagent_not_supported", `{"session_id":"thr_123","hook_event_name":"SubagentStart"}`, "unsupported_event"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := taskDir(t)
			code, stdout, stderr := invokeHook(context.Background(), dir, tc.input)
			if code != 1 || stdout != "" || stderr != "g0-hook: "+tc.category+"\n" {
				t.Errorf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Error("invalid input created a record")
			}
		})
	}
}

func TestConcurrentEventsNeverOverwrite(t *testing.T) {
	dir := taskDir(t)
	var wg sync.WaitGroup
	results := make(chan string, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			input := fmt.Sprintf(`{"session_id":"thr_123","hook_event_name":"UserPromptSubmit","turn_id":"turn_%d"}`, i)
			code, stdout, stderr := invokeHook(context.Background(), dir, input)
			if code != 0 || stdout != "" || stderr != "" {
				results <- fmt.Sprintf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
		}(i)
	}
	wg.Wait()
	close(results)
	for result := range results {
		t.Error(result)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 16 {
		t.Fatalf("expected 16 independent events; count=%d err=%v", len(entries), err)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var value struct {
			TurnID string `json:"turn_id"`
		}
		if json.Unmarshal(data, &value) != nil || value.TurnID == "" || seen[value.TurnID] {
			t.Fatal("event overwritten or incomplete")
		}
		seen[value.TurnID] = true
	}
}

func TestDirectoryMissingAndSymlinksCannotEscape(t *testing.T) {
	for _, mode := range []string{"missing", "final_link", "parent_link", "not_directory", "not_writable"} {
		t.Run(mode, func(t *testing.T) {
			root, outside := taskDir(t), taskDir(t)
			destination := filepath.Join(root, "output")
			switch mode {
			case "final_link":
				if err := os.Symlink(outside, destination); err != nil {
					t.Fatal(err)
				}
			case "parent_link":
				if err := os.Mkdir(filepath.Join(outside, "output"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(root, "parent")); err != nil {
					t.Fatal(err)
				}
				destination = filepath.Join(root, "parent", "output")
			case "not_directory":
				if err := os.WriteFile(destination, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "not_writable":
				if err := os.Mkdir(destination, 0500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(destination, 0700) })
			}
			code, stdout, stderr := invokeHook(context.Background(), destination, `{"session_id":"thr_123","hook_event_name":"SessionEnd"}`)
			if code != 1 || stdout != "" || stderr != "g0-hook: io_error\n" {
				t.Errorf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			entries, err := os.ReadDir(outside)
			want := 0
			if mode == "parent_link" {
				want = 1
				children, _ := os.ReadDir(filepath.Join(outside, "output"))
				if len(children) != 0 {
					t.Error("record escaped through parent link")
				}
			}
			if err != nil || len(entries) != want {
				t.Error("record escaped through link")
			}
			if mode == "missing" {
				if _, err := os.Lstat(destination); !os.IsNotExist(err) {
					t.Error("missing output directory was created")
				}
			}
		})
	}
}

func TestArgumentsNeverReflected(t *testing.T) {
	for _, args := range [][]string{nil, {"--output-dir", "relative", "--nonce", "test-nonce"}, {"--output-dir", "/unused", "--nonce", ""}, {"--output-dir", "/unused", "--nonce", "bad\nnonce"}, {"--output-dir", "/unused", "--nonce", strings.Repeat("x", 129)}, {"--output-dir", "/unused", "--nonce", "n", privateCanary}, {"--" + privateCanary}} {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
		if code != 2 || stdout.Len() != 0 || stderr.String() != "g0-hook: invalid_arguments\n" {
			t.Errorf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	}
}

func TestBlockedInputHasTwoSecondBudget(t *testing.T) {
	dir := taskDir(t)
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	var stdout, stderr bytes.Buffer
	start := time.Now()
	code := run(context.Background(), []string{"--output-dir", dir, "--nonce", "test-nonce"}, reader, &stdout, &stderr)
	elapsed := time.Since(start)
	if code != 1 || stdout.Len() != 0 || stderr.String() != "g0-hook: timeout\n" {
		t.Errorf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if elapsed < 1800*time.Millisecond || elapsed > 2500*time.Millisecond {
		t.Errorf("expected 2s input budget, got %s", elapsed)
	}
}

// A child-only file-size limit creates a real partial write failure without
// changing the parent process, filling a disk, or touching a real Hook.
func TestWriteFailureDoesNotSucceed(t *testing.T) {
	if len(os.Args) >= 3 && os.Args[len(os.Args)-2] == "hook-write-failure-child" {
		signal.Ignore(syscall.SIGXFSZ)
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 64, Max: 64}); err != nil {
			os.Exit(9)
		}
		code := run(context.Background(), []string{"--output-dir", os.Args[len(os.Args)-1], "--nonce", "test-nonce"}, strings.NewReader(`{"session_id":"thr_123","hook_event_name":"SessionEnd"}`), os.Stdout, os.Stderr)
		os.Exit(code)
	}
	dir := taskDir(t)
	command := exec.Command(os.Args[0], "-test.run=^TestWriteFailureDoesNotSucceed$", "--", "hook-write-failure-child", dir)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 || stdout.Len() != 0 || stderr.String() != "g0-hook: io_error\n" {
		t.Errorf("child error=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Error("failed partial event was retained as a complete JSON record")
	}
}
