//go:build darwin

package process

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestDarwinObservationForkDeniedHelper(t *testing.T) {
	if os.Getenv("DARWIN_OBSERVATION_HELPER") != "1" {
		return
	}
	pid, golden := os.Getpid(), os.Getenv("DARWIN_OBSERVATION_BIRTH")
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
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": ok, "birth": birth, "context_birth": contextBirth, "ancestry_self": ancestry[pid], "quiescent": quiet, "cancelled_error": textErr(cancelledErr)})
	if !ok {
		os.Exit(2)
	}
}

func textErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestDarwinObservationsDoNotForkAfterSandbox(t *testing.T) {
	if os.Getenv("DARWIN_OBSERVATION_HELPER") == "1" {
		return
	}
	golden, err := Birth(os.Getpid())
	if err != nil || golden == "" {
		t.Fatal(err)
	}
	data := filepath.Join("..", "..", "..", "..", "tasks", "darwin-process-observation", "data")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	raw := filepath.Join(data, "sandbox-exec-raw.json")
	_ = os.Remove(raw)
	cmd := exec.Command("/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)(deny process-fork)", os.Args[0], "-test.run=^TestDarwinObservationForkDeniedHelper$")
	cmd.Env = append(os.Environ(), "LANG=C", "LC_ALL=C", "DARWIN_OBSERVATION_HELPER=1", "DARWIN_OBSERVATION_BIRTH="+golden)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	runErr := cmd.Wait()
	out := stdout.Bytes()
	if err := os.WriteFile(raw, out, 0600); err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatalf("fork-denied helper: %v raw=%s", runErr, out)
	}
	var got struct {
		OK             bool
		Birth          string
		ContextBirth   string `json:"context_birth"`
		AncestrySelf   string `json:"ancestry_self"`
		Quiescent      bool
		CancelledError string `json:"cancelled_error"`
	}
	if err := json.Unmarshal(out, &got); err != nil || !got.OK || got.Birth != golden || got.ContextBirth != golden || got.AncestrySelf != golden || got.Quiescent || got.CancelledError == "" {
		t.Fatalf("helper=%s err=%v", out, err)
	}
}
