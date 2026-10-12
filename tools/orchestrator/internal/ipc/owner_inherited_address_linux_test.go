//go:build linux

package ipc

import (
	"net"
	"testing"
)

func TestOwnerInheritedControlRejectsConnectedAbstractNames(t *testing.T) {
	for _, name := range []string{"@", "@owner-inherited-named"} {
		t.Run(name, func(t *testing.T) {
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: name, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			client, err := net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			accepted, err := listener.AcceptUnix()
			if err != nil {
				t.Fatal(err)
			}
			defer accepted.Close()
			for _, conn := range []*net.UnixConn{client, accepted} {
				file, err := conn.File()
				if err != nil {
					t.Fatal(err)
				}
				fd := ownerInheritedDup(t, file)
				_ = file.Close()
				if stream, err := TakeOwnerControlFD(fd); err == nil || stream != nil {
					if stream != nil {
						_ = stream.Close()
					}
					t.Fatal("abstract named connection accepted as anonymous pair")
				}
				ownerInheritedClosed(t, fd)
			}
		})
	}
}
