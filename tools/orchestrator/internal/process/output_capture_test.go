package process

import (
	"strings"
	"sync"
	"testing"
)

type outputByteCounter struct{ bytes int }

func (c *outputByteCounter) Write(p []byte) (int, error) { c.bytes += len(p); return len(p), nil }

func TestOutputSnapshotTracksActualLossAcrossBothStreams(t *testing.T) {
	counter := &outputByteCounter{}
	h := &Handle{outputLimit: 16, stdout: counter}
	stdout, stderr := captureWriter{h: h, stdout: true}, captureWriter{h: h}
	if n, err := stdout.Write([]byte("0123456789")); n != 10 || err != nil {
		t.Fatal(n, err)
	}
	before, truncated := h.OutputSnapshot()
	if before != "0123456789" || truncated {
		t.Fatal("incomplete capture before reaching cap")
	}
	if n, err := stderr.Write([]byte("abcdef")); n != 6 || err != nil {
		t.Fatal(n, err)
	}
	if output, truncated := h.OutputSnapshot(); output != "0123456789abcdef" || truncated {
		t.Fatal("exact shared cap was incorrectly truncated", output, truncated)
	}
	if n, err := stderr.Write([]byte("!")); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	flood := []byte(strings.Repeat("x", 1024*1024))
	if n, err := stdout.Write(flood); n != len(flood) || err != nil {
		t.Fatal("capture limit blocked downstream drain", n, err)
	}
	if output, truncated := h.OutputSnapshot(); output != "0123456789abcdef" || !truncated || counter.bytes != 10+len(flood) {
		t.Fatal("lost bytes/drain were not accounted for", len(output), truncated, counter.bytes)
	}
	if before != "0123456789" || h.Output() != "0123456789abcdef" {
		t.Fatal("snapshot mutated or legacy Output changed")
	}
}

func TestOutputSnapshotIsConsistentDuringConcurrentCapture(t *testing.T) {
	h := &Handle{outputLimit: 1024}
	var group sync.WaitGroup
	defer group.Wait()
	for _, stdout := range []bool{false, true} {
		group.Add(1)
		go func(stdout bool) {
			defer group.Done()
			writer := captureWriter{h: h, stdout: stdout}
			for i := 0; i < 1000; i++ {
				_, _ = writer.Write([]byte("abcdefghi"))
			}
		}(stdout)
	}
	for i := 0; i < 1000; i++ {
		output, truncated := h.OutputSnapshot()
		if len(output) > 1024 || (truncated && len(output) != 1024) {
			t.Fatal("inconsistent capture snapshot", len(output), truncated)
		}
	}
	group.Wait()
	if output, truncated := h.OutputSnapshot(); len(output) != 1024 || !truncated {
		t.Fatal("combined stdout/stderr loss missing", len(output), truncated)
	}
}
