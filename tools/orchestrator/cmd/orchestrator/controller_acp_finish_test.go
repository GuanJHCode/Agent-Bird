package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestControllerACPPipeSignalSubscriptionPreservesOtherListeners(t *testing.T) {
	if os.Getenv("ACP_PIPE_SUBSCRIBER_ACTOR") != "1" {
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_ = read.Close()
		defer write.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestControllerACPPipeSignalSubscriptionPreservesOtherListeners$")
		cmd.Env = environmentWith(os.Environ(), "ACP_PIPE_SUBSCRIBER_ACTOR", "1")
		cmd.Stdout = write
		var diagnostics bytes.Buffer
		cmd.Stderr = &diagnostics
		if err = cmd.Run(); err != nil || diagnostics.String() != "owned-pipe-subscriber-pass\n" {
			t.Fatalf("original pipe helper Wait: %v diagnostics=%s", err, diagnostics.String())
		}
		return
	}
	other := make(chan os.Signal, 1)
	signal.Notify(other, syscall.SIGPIPE)
	defer signal.Stop(other)
	stop := retainACPOutputFailures()
	stop()
	if _, err := os.Stdout.Write([]byte("owned broken fd1\n")); !errors.Is(err, syscall.EPIPE) {
		os.Exit(41)
	}
	select {
	case got := <-other:
		if got != syscall.SIGPIPE {
			os.Exit(42)
		}
	case <-time.After(time.Second):
		os.Exit(43)
	}
	_, _ = os.Stderr.WriteString("owned-pipe-subscriber-pass\n")
	os.Exit(0) // The testing framework itself must not write PASS to the broken fd1.
}

func TestControllerACPCleanupErrorHasPublicPriority(t *testing.T) {
	err := &controllerACPCleanupError{cause: errors.Join(codeError("bridge_cancelled"), process.ErrProcessTreeUnknown)}
	if got := errorCode(err); got != "controller_acp_cleanup_incomplete" {
		t.Fatalf("live ownership warning hidden by cause: %s", got)
	}
}

func TestControllerACPFinalMetadataFailureCannotMasqueradeAsPersistedUncertainty(t *testing.T) {
	root := privateTaskTemp(t)
	s := &controllerACPSession{ctx: context.Background(), handlePath: filepath.Join(root, "handle"), metadata: controllerACPMetadata{Version: 1, RunID: "run", Status: "active"}, metadataWritten: true, uncertain: true}
	foreign := s.metadata
	foreign.Status = "foreign"
	if err := writeExclusiveJSON(s.metadataPath(), foreign); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.metadataPath())
	var out bytes.Buffer
	err := s.finish(codeError("controller_acp_protocol"), &out)
	if errorCode(err) != "controller_acp_metadata_persistence_failed" {
		t.Fatalf("metadata failure hidden: %v", err)
	}
	var message struct {
		Status    string `json:"status"`
		Intended  string `json:"intended_status"`
		Persisted bool   `json:"metadata_persisted"`
	}
	if json.Unmarshal(out.Bytes(), &message) != nil || message.Status != "controller_acp_metadata_persistence_failed" || message.Intended != "uncertain" || message.Persisted {
		t.Fatalf("false durable terminal assertion: %s", out.Bytes())
	}
	after, _ := os.ReadFile(s.metadataPath())
	if !bytes.Equal(before, after) {
		t.Fatal("foreign metadata replaced")
	}
}
