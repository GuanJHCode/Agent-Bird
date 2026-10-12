package process

import (
	"bytes"
	"io"
	"os"
	"testing"
	"time"
)

type observedOutput struct {
	stream OutputStream
	data   string
}

func TestCaptureWriterObservesOriginalBytesOutsideHandleLock(t *testing.T) {
	var observed []observedOutput
	var downstream bytes.Buffer
	h := &Handle{outputLimit: 4, stdout: &downstream}
	h.outputObserver = func(stream OutputStream, p []byte) {
		_ = h.Output()
		observed = append(observed, observedOutput{stream: stream, data: string(p)})
	}
	stdout, stderr := captureWriter{h: h, stdout: true}, captureWriter{h: h}

	type writeResult struct {
		n   int
		err error
	}
	written := make(chan writeResult, 1)
	go func() {
		n, err := stdout.Write([]byte("abcdef"))
		written <- writeResult{n: n, err: err}
	}()
	select {
	case result := <-written:
		if result.n != 6 || result.err != nil {
			t.Fatalf("stdout write n=%d err=%v", result.n, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("observer calling Output deadlocked capture writer")
	}
	if n, err := stderr.Write([]byte("gh")); n != 2 || err != nil {
		t.Fatalf("stderr write n=%d err=%v", n, err)
	}
	if output, truncated := h.OutputSnapshot(); output != "abcd" || !truncated {
		t.Fatalf("legacy summary changed: output=%q truncated=%v", output, truncated)
	}
	if downstream.String() != "abcdef" {
		t.Fatalf("stdout downstream changed: %q", downstream.String())
	}
	want := []observedOutput{{OutputStdout, "abcdef"}, {OutputStderr, "gh"}}
	if len(observed) != len(want) {
		t.Fatalf("observations=%#v", observed)
	}
	for i := range want {
		if observed[i] != want[i] {
			t.Fatalf("observation[%d]=%#v want %#v", i, observed[i], want[i])
		}
	}
}

func TestDuplexReadObservesOnlyOriginalStdoutRead(t *testing.T) {
	parentRead, childStdout, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	childStdin, parentWrite, err := os.Pipe()
	if err != nil {
		_ = childStdout.Close()
		_ = parentRead.Close()
		t.Fatal(err)
	}
	defer childStdout.Close()
	defer childStdin.Close()
	defer parentRead.Close()
	defer parentWrite.Close()

	var observed []observedOutput
	stream := &duplexStream{
		reader: parentRead,
		writer: parentWrite,
		outputObserver: func(kind OutputStream, p []byte) {
			observed = append(observed, observedOutput{kind, string(p)})
		},
	}
	if _, err := childStdout.Write([]byte("reply")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("reply"))
	if n, err := io.ReadFull(stream, got); n != len(got) || err != nil || string(got) != "reply" {
		t.Fatalf("original stdout read n=%d err=%v data=%q", n, err, got)
	}
	if _, err := stream.Write([]byte("request")); err != nil {
		t.Fatal(err)
	}
	input := make([]byte, len("request"))
	if n, err := io.ReadFull(childStdin, input); n != len(input) || err != nil || string(input) != "request" {
		t.Fatalf("original stdin write n=%d err=%v data=%q", n, err, input)
	}
	if len(observed) != 1 || observed[0] != (observedOutput{OutputStdout, "reply"}) {
		t.Fatalf("duplex observation=%#v", observed)
	}
}
