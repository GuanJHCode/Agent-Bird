package host

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

type ownerCloseTestConn struct {
	net.Conn
	prefix []byte
	err    error
}

func TestOwnerTransportFramesPreserveMessageBeforeReset(t *testing.T) {
	var wire bytes.Buffer
	message := ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerTransportControl, RequestID: "last-frame"}
	if err := ipc.Write(&wire, message); err != nil {
		t.Fatal(err)
	}
	size := wire.Len()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := readOwnerTransportFrames(ctx, &ownerCloseTestConn{prefix: wire.Bytes(), err: syscall.ECONNRESET})
	first := <-frames
	if first.err != nil || first.message.RequestID != message.RequestID || first.bytesRead != size {
		t.Fatalf("complete frame lost: %+v", first)
	}
	last := <-frames
	if last.bytesRead != 0 || !IsInitialOwnerControlReset(ownerTransportControlReadError(last, true)) {
		t.Fatalf("frame byte count carried over: %+v", last)
	}
	if IsInitialOwnerControlReset(syscall.ECONNRESET) || IsInitialOwnerControlReset(io.ErrUnexpectedEOF) {
		t.Fatal("unmarked transport error accepted")
	}
	if _, open := <-frames; open {
		t.Fatal("reader did not finish")
	}
}

func (c *ownerCloseTestConn) Read(p []byte) (int, error) {
	if len(c.prefix) != 0 {
		n := copy(p, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return 0, c.err
}

func (c *ownerCloseTestConn) Close() error { return nil }

func TestOwnerTransportInitialCloseRequiresFrameBoundary(t *testing.T) {
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, 20)
	reset := &net.OpError{Op: "read", Net: "unix", Err: syscall.ECONNRESET}
	for _, tc := range []struct {
		name                       string
		prefix                     []byte
		err                        error
		initial, normalized, clean bool
	}{
		{"initial_reset", nil, reset, true, true, false},
		{"replacement_reset", nil, reset, false, false, false},
		{"initial_eof", nil, io.EOF, true, false, true},
		{"replacement_eof", nil, io.EOF, false, false, true},
		{"partial_header_reset", header[:2], reset, true, false, false},
		{"empty_payload_reset", header, reset, true, false, false},
		{"partial_payload_reset", append(append([]byte{}, header...), '{'), reset, true, false, false},
		{"partial_header_eof", header[:2], io.EOF, true, false, false},
		{"empty_payload_eof", header, io.EOF, true, false, false},
		{"partial_payload_eof", append(append([]byte{}, header...), '{'), io.EOF, true, false, false},
		{"other_socket_error", nil, syscall.EPIPE, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			frames := readOwnerTransportFrames(ctx, &ownerCloseTestConn{prefix: append([]byte{}, tc.prefix...), err: tc.err})
			frame := <-frames
			err := ownerTransportControlReadError(frame, tc.initial)
			if IsInitialOwnerControlReset(err) != tc.normalized || (err == nil) != tc.clean {
				t.Fatalf("close classification=%v normalized=%v", err, IsInitialOwnerControlReset(err))
			}
			if !tc.clean && errors.Is(tc.err, syscall.ECONNRESET) && !errors.Is(err, syscall.ECONNRESET) {
				t.Fatal("reset cause lost", err)
			}
			if len(tc.prefix) > 0 && errors.Is(tc.err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatal("truncated frame accepted", err)
			}
			if _, open := <-frames; open {
				t.Fatal("reader did not finish")
			}
		})
	}
}
