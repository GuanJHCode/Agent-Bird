package process

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

type ancestryOSReady struct {
	PID, Parent, UID int
	Birth            string
}

func TestAncestryContextRealOSSelfAndCompatibility(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	birth, err := Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	result, err := AncestryContext(ctx, os.Getpid())
	if err != nil || result[os.Getpid()] != birth {
		t.Fatalf("real self identity missing: result=%v err=%v", result, err)
	}
	legacy, err := Ancestry(os.Getpid())
	if err != nil || !reflect.DeepEqual(legacy, result) {
		t.Fatalf("legacy ancestry changed: legacy=%v result=%v err=%v", legacy, result, err)
	}
}

func TestAncestryContextRealOSParentChildAndGone(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestAncestryFixtureProcess$")
	cmd.Env = append(os.Environ(), "ANCESTRY_OS_FIXTURE=1", "GORACE="+os.Getenv("GORACE")+" atexit_sleep_ms=0")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = stdout.Close()
		if !waited {
			// This child stays waitable until this sole Wait; no PID marker/PGID authority.
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	pipe, ok := stdout.(*os.File)
	if !ok {
		t.Fatal("fixture stdout is not a pollable OS pipe")
	}
	if err := pipe.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var ready ancestryOSReady
	if err := json.NewDecoder(stdout).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	if ready.PID != cmd.Process.Pid || ready.Parent != os.Getpid() || ready.UID != os.Geteuid() || ready.Birth == "" {
		t.Fatalf("untrusted real child receipt: %+v", ready)
	}
	parentBirth, err := Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := AncestryContext(ctx, ready.PID)
	if err != nil || result[ready.PID] != ready.Birth || result[os.Getpid()] != parentBirth {
		t.Fatalf("real parent/child ancestry mismatch: result=%v err=%v", result, err)
	}
	canceled, cancelQuery := context.WithCancel(context.Background())
	cancelQuery()
	if got, err := AncestryContext(canceled, ready.PID); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("real canceled query admitted: result=%v err=%v", got, err)
	}
	if _, err := fmt.Fprintln(stdin, "exit"); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatal(err)
	}
	if got, err := AncestryContext(ctx, ready.PID); err == nil || got != nil {
		t.Fatalf("departed child admitted: result=%v err=%v", got, err)
	}
}

func TestAncestryFixtureProcess(t *testing.T) {
	if os.Getenv("ANCESTRY_OS_FIXTURE") == "" {
		return
	}
	birth, err := Birth(os.Getpid())
	if err != nil {
		os.Exit(71)
	}
	if err := json.NewEncoder(os.Stdout).Encode(ancestryOSReady{os.Getpid(), os.Getppid(), os.Geteuid(), birth}); err != nil {
		os.Exit(72)
	}
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		os.Exit(73)
	}
}
