package process

import (
	"errors"
	"io"
	"testing"
	"time"
)

type closeWriter interface {
	CloseWrite() error
}

func TestDuplexStreamCloseWriteClosesOnlyStdinAndRetainsStdoutTail(t *testing.T) {
	pipes, err := newDuplexPipes()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = pipes.stream.Close()
		_ = pipes.closeChildren()
	})

	stream, ok := any(pipes.stream).(closeWriter)
	if !ok {
		t.Fatal("duplex stream does not support runtime half-close")
	}
	if err := stream.CloseWrite(); err != nil {
		t.Fatal(err)
	}

	stdinDone := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(pipes.childRead)
		stdinDone <- err
	}()
	select {
	case err := <-stdinDone:
		if err != nil {
			t.Fatalf("child stdin EOF: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CloseWrite did not deliver child stdin EOF")
	}

	if _, err := pipes.childWrite.Write([]byte("tail")); err != nil {
		t.Fatalf("child stdout tail: %v", err)
	}
	output := make([]byte, 4)
	if _, err := io.ReadFull(pipes.stream, output); err != nil {
		t.Fatalf("parent stdout after CloseWrite: %v", err)
	}
	if string(output) != "tail" {
		t.Fatalf("stdout tail=%q", output)
	}

	if err := stream.CloseWrite(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("repeated CloseWrite: %v", err)
	}
}

func TestDuplexStreamCloseAfterCloseWriteIsIdempotent(t *testing.T) {
	pipes, err := newDuplexPipes()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pipes.closeChildren() })
	if err := pipes.stream.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	first := pipes.stream.Close()
	second := pipes.stream.Close()
	if first != second {
		t.Fatalf("Close did not retain its original result: first=%v second=%v", first, second)
	}
	if _, err := pipes.childWrite.Write([]byte("after full close")); err == nil {
		t.Fatal("full Close retained stdout reader")
	}
}
