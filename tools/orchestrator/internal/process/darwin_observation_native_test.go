//go:build darwin

package process

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDarwinObservationForkDeniedHelper(t *testing.T) {
	if os.Getenv("DARWIN_OBSERVATION_HELPER") != "1" {
		t.Skip("owned helper only")
	}
	var golden string
	if err := json.NewDecoder(os.Stdin).Decode(&golden); err != nil {
		panic(err)
	}
	pid := os.Getpid()
	birth, birthErr := Birth(pid)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	contextBirth, contextErr := BirthContext(ctx, pid)
	ancestry, ancestryErr := AncestryContext(ctx, pid)
	leaderErr := MatchWaitableGroupLeader(ctx, pid, golden)
	quiet, quietErr := GroupQuiescent(ctx, pid)
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	_, cancelledErr := BirthContext(cancelled, pid)
	ok := birthErr == nil && birth == golden && contextErr == nil && contextBirth == golden && ancestryErr == nil && ancestry[pid] == golden && leaderErr == nil && quietErr == nil && !quiet && cancelledErr != nil
	result := map[string]any{"ok": ok, "pid": pid, "golden": golden, "birth": birth, "birth_error": fmt.Sprint(birthErr), "context_birth": contextBirth, "context_error": fmt.Sprint(contextErr), "ancestry_self": ancestry[pid], "ancestry_error": fmt.Sprint(ancestryErr), "leader_error": fmt.Sprint(leaderErr), "quiescent": quiet, "quiescent_error": fmt.Sprint(quietErr), "cancelled_error": fmt.Sprint(cancelledErr)}
	_ = json.NewEncoder(os.Stdout).Encode(result)
	if !ok {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestDarwinObservationsDoNotForkAfterSandbox(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)(deny process-fork)", os.Args[0], "-test.run=^TestDarwinObservationForkDeniedHelper$")
	cmd.Env = append(os.Environ(), "LANG=C", "LC_ALL=C", "DARWIN_OBSERVATION_HELPER=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// The parent obtains an independent ps golden for the actual waiting child.
	// Only the sandboxed child is forbidden to fork; no parent birth is substituted.
	goldenCmd := exec.CommandContext(ctx, "/bin/ps", "-o", "lstart=", "-p", strconv.Itoa(cmd.Process.Pid))
	goldenCmd.Env = append(os.Environ(), "LANG=C", "LC_ALL=C")
	raw, goldenErr := goldenCmd.Output()
	golden := strings.TrimSpace(string(raw))
	sendErr := json.NewEncoder(input).Encode(golden)
	_ = input.Close()
	waitErr := cmd.Wait()
	t.Logf("child_pid=%d original_wait=%v golden=%q raw=%s stderr=%s", cmd.Process.Pid, waitErr, golden, stdout.Bytes(), stderr.Bytes())
	if goldenErr != nil || sendErr != nil || golden == "" || waitErr != nil {
		t.Fatalf("golden=%v send=%v wait=%v", goldenErr, sendErr, waitErr)
	}
	var got struct {
		OK     bool
		Birth  string
		Golden string
	}
	if err = json.Unmarshal(stdout.Bytes(), &got); err != nil || !got.OK || got.Birth != golden || got.Golden != golden {
		t.Fatalf("helper=%s decode=%v", stdout.Bytes(), err)
	}
}
