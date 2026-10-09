package ipc

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// Ordinary process signals must not turn still-open, empty inherited endpoints
// into malformed IPC. Exercise a finite burst, not an unlimited signal flood.
// No bootstrap or control payload is written or read.
func TestOwnerLaunchDescriptorValidationSurvivesSignals(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	parent, child, err := NewOwnerControlPair()
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	defer child.Close()
	handoff := &OwnerLaunchFDs{files: []*os.File{reader, child}}
	received := make(chan os.Signal, 64)
	signal.Notify(received, syscall.SIGUSR1)
	defer signal.Stop(received)
	stop := make(chan struct{})
	sent := make(chan struct{})
	drained := make(chan struct{})
	go func() {
		defer close(sent)
		for burst := 0; burst < 8; burst++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = syscall.Kill(os.Getpid(), syscall.SIGUSR1)
			time.Sleep(10 * time.Microsecond)
		}
	}()
	go func() {
		defer close(drained)
		for {
			select {
			case <-stop:
				return
			case <-received:
			}
		}
	}()
	defer func() { close(stop); <-sent; <-drained }()
	for i := 0; i < 4096; i++ {
		if err = handoff.ValidateEmpty(); err != nil {
			t.Fatalf("empty endpoints rejected during signals at iteration %d: %v", i, err)
		}
	}
}

func TestOwnerLaunchDescriptorReceiveRetriesInterruptedSyscall(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued_rights", true: "cancelled"}[cancelled], func(t *testing.T) {
			transport, endpoint, err := NewOwnerControlPair()
			if err != nil {
				t.Fatal(err)
			}
			defer transport.Close()
			peer, err := TakeOwnerControlFD(ownerInheritedDup(t, endpoint))
			endpoint.Close()
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			parent, child, err := NewOwnerControlPair()
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			defer child.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err = SendOwnerLaunchFDs(ctx, transport, reader, child); err != nil {
				t.Fatal(err)
			}
			calls := 0
			receive := func(fd int, payload, oob []byte, flags int) (int, int, int, syscall.Sockaddr, error) {
				calls++
				if calls == 1 {
					if cancelled {
						cancel()
					}
					return 0, 0, 0, nil, syscall.EINTR
				}
				return syscall.Recvmsg(fd, payload, oob, flags)
			}
			handoff, err := receiveOwnerLaunchFDs(ctx, peer, receive)
			if cancelled {
				if !errors.Is(err, context.Canceled) || handoff != nil || calls != 1 {
					t.Fatal("interrupted receive ignored cancellation", calls, handoff, err)
				}
				return
			}
			if err != nil || handoff == nil {
				t.Fatal("queued rights lost after interruption", calls, err)
			}
			defer handoff.Close()
			if calls != 2 || len(handoff.Files()) != 2 {
				t.Fatal("unexpected interrupted receive", calls, len(handoff.Files()))
			}
			for _, file := range handoff.Files() {
				ownerInheritedCloexec(t, file.Fd())
			}
		})
	}
}
