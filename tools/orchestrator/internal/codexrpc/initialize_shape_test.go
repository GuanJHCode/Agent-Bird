package codexrpc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"
)

// Synthetic values reproduce only the retained native initialize shape. No
// original response, native process, auth/config reference or filesystem actor.
const initializeShapeResult = `{"codexHome":"/owned/codex","platformFamily":"unix","platformOs":"macos","userAgent":"synthetic-agent"}`
const initializeShapeReply = `{"id":"local-1","result":` + initializeShapeResult + `}`

func TestObservedInitializeShapeDecodesWithoutJSONRPCVersion(t *testing.T) {
	f, err := decodeFrame([]byte(initializeShapeReply))
	if err != nil || f.kind != resultFrame || !bytes.Equal(f.result, []byte(initializeShapeResult)) {
		t.Fatalf("retained initialize shape rejected: %v", err)
	}
	if id, ok := f.id.StringValue(); !ok || id != "local-1" {
		t.Fatal("synthetic string ID changed")
	}
}

func initializeShapeCall(f *earlyReplyFixture) <-chan outcome {
	done := make(chan outcome, 1)
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		body, err := f.c.Call(f.ctx, "initialize", json.RawMessage(`{"clientInfo":{"name":"synthetic"}}`))
		done <- outcome{result: body, err: err}
	}()
	return done
}

func initializeShapeWritingJob(t *testing.T, f *earlyReplyFixture) *writeJob {
	t.Helper()
	awaitSignal(t, f.ctx, f.stream.started)
	request, err := readFixtureFrame(bufio.NewReader(f.peer))
	if err != nil || string(request["id"]) != `"local-1"` || string(request["method"]) != `"initialize"` {
		t.Fatal("real initialize request boundary missing")
	}
	awaitSignal(t, f.ctx, f.stream.complete)
	f.c.mu.Lock()
	call := f.c.pending["local-1"]
	var job *writeJob
	if call != nil {
		job = call.job
	}
	ok := job != nil && job.stage == writing
	f.c.mu.Unlock()
	if !ok {
		t.Fatal("real initialize call not held in writing")
	}
	return job
}

func TestObservedInitializeShapeAcceptedInBothWriteStages(t *testing.T) {
	for _, stage := range []string{"writing", "written"} {
		t.Run(stage, func(t *testing.T) {
			f := newEarlyReplyFixture(t, true)
			done := initializeShapeCall(f)
			job := initializeShapeWritingJob(t, f)
			if stage == "written" {
				f.stream.releaseReturn()
				// A later actual writer job can complete only after the original
				// initialize job becomes written. This is an ordering barrier.
				notified := make(chan error, 1)
				f.wg.Add(1)
				go func() {
					defer f.wg.Done()
					notified <- f.c.Notify(f.ctx, "fixture/requestWritten", nil)
				}()
				marker, err := readFixtureFrame(bufio.NewReader(f.peer))
				if err != nil || string(marker["method"]) != `"fixture/requestWritten"` {
					t.Fatal("real subsequent writer barrier missing")
				}
				select {
				case err := <-notified:
					if err != nil {
						t.Fatal(err)
					}
				case <-f.ctx.Done():
					t.Fatal("subsequent writer did not join")
				}
				f.c.mu.Lock()
				written := job.stage == written && f.c.pending["local-1"] == job.call
				f.c.mu.Unlock()
				if !written {
					t.Fatal("initialize not proven written before reply")
				}
			}
			f.replyAndObserve(t, initializeShapeReply)
			if stage == "writing" {
				f.requireHeld(t, job)
				f.stream.releaseReturn()
			}
			value := f.result(t, done)
			if value.err != nil || !bytes.Equal(value.result, []byte(initializeShapeResult)) || f.c.Err() != nil {
				t.Fatalf("matched synthetic initialize reply failed at %s: %v", stage, value.err)
			}
			t.Logf("complete matched initialize accepted in actual %s stage; Close cleanup joins original pumps", stage)
		})
	}
}

func TestLaterMalformedFrameCanFailAfterAcceptedInitializeShape(t *testing.T) {
	f := newEarlyReplyFixture(t, true)
	done := initializeShapeCall(f)
	job := initializeShapeWritingJob(t, f)
	f.replyAndObserve(t, initializeShapeReply)
	f.requireHeld(t, job) // first matched reply really accepted, Call still owned
	_, _ = io.WriteString(f.peer, "\n")
	requireTerminal(t, f.c, f.ctx, ErrProtocol)
	value := f.result(t, done)
	if !errors.Is(value.err, ErrProtocol) || value.result != nil {
		t.Fatal("post-reply malformed frame did not preserve fail-closed boundary")
	}
	t.Log("causal synthetic contrast only: accepted initialize then blank frame produces protocol failure; no claim native emitted a blank frame")
}
