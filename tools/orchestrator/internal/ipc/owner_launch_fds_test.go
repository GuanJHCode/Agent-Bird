package ipc

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestOwnerLaunchDescriptorHandoff(t *testing.T) {
	for _, variant := range []string{"empty_pair", "early_bootstrap", "early_control", "wrong_direction", "extra_descriptor", "cancelled"} {
		t.Run(variant, func(t *testing.T) {
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
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			defer w.Close()
			control, child, err := NewOwnerControlPair()
			if err != nil {
				t.Fatal(err)
			}
			defer control.Close()
			defer child.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			fds := []int{int(r.Fd()), int(child.Fd())}
			switch variant {
			case "early_bootstrap":
				if _, err = w.Write([]byte("private-not-for-coordinator")); err != nil {
					t.Fatal(err)
				}
			case "early_control":
				if _, err = control.Write([]byte("private-not-for-coordinator")); err != nil {
					t.Fatal(err)
				}
			case "wrong_direction":
				fds[0] = int(w.Fd())
			case "extra_descriptor":
				fds = append(fds, int(w.Fd()))
			case "cancelled":
				cancel()
			}
			if variant == "empty_pair" {
				err = SendOwnerLaunchFDs(ctx, transport, r, child)
			} else if variant != "cancelled" {
				_, _, err = transport.WriteMsgUnix([]byte{ownerLaunchFDMarker}, syscall.UnixRights(fds...), nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			handoff, err := ReceiveOwnerLaunchFDs(ctx, peer)
			if variant != "empty_pair" {
				if err == nil || handoff != nil {
					t.Fatal("invalid descriptor handoff accepted", variant)
				}
				if variant == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				// Validation checks readiness without consuming private payload.
				if variant == "early_bootstrap" {
					got := make([]byte, len("private-not-for-coordinator"))
					if _, err = io.ReadFull(r, got); err != nil || string(got) != "private-not-for-coordinator" {
						t.Fatal("bootstrap payload consumed", err)
					}
				}
				if variant == "extra_descriptor" {
					// Excess SCM_RIGHTS must be closed as well as the valid pair.
					w.Close()
					child.Close()
					r.SetReadDeadline(time.Now().Add(time.Second))
					control.SetReadDeadline(time.Now().Add(time.Second))
					var one [1]byte
					if _, err = r.Read(one[:]); !errors.Is(err, io.EOF) {
						t.Fatal("extra writer descriptor leaked", err)
					}
					if _, err = control.Read(one[:]); !errors.Is(err, io.EOF) {
						t.Fatal("rejected child control descriptor leaked", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer handoff.Close()
			files := handoff.Files()
			if len(files) != 2 {
				t.Fatal("wrong child descriptors")
			}
			for _, f := range files {
				ownerInheritedCloexec(t, f.Fd())
			}
			// The received pipe/socket still refer to the original channels.
			if _, err = w.Write([]byte("source")); err != nil {
				t.Fatal(err)
			}
			got := make([]byte, 6)
			if _, err = io.ReadFull(files[0], got); err != nil || string(got) != "source" {
				t.Fatal(err)
			}
			childConn, err := net.FileConn(files[1])
			if err != nil {
				t.Fatal(err)
			}
			defer childConn.Close()
			if _, err = control.Write([]byte("direct")); err != nil {
				t.Fatal(err)
			}
			if _, err = io.ReadFull(childConn, got); err != nil || string(got) != "direct" {
				t.Fatal(err)
			}
			ownedFDs := []int{int(files[0].Fd()), int(files[1].Fd())}
			if err = handoff.Close(); err != nil {
				t.Fatal(err)
			}
			for _, fd := range ownedFDs {
				ownerInheritedClosed(t, fd)
			}
			if handoff.Files() != nil {
				t.Fatal("closed handoff can be launched again")
			}
		})
	}
}
