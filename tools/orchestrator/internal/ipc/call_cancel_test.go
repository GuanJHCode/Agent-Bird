package ipc

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCallStopsConnectedIOOnContext(t *testing.T) {
	for _, partial := range []bool{false, true} {
		for _, deadline := range []bool{false, true} {
			name := "cancel"
			if deadline {
				name = "deadline"
			}
			if partial {
				name += "-partial"
			}
			t.Run(name, func(t *testing.T) {
				dir, e := os.MkdirTemp("", "ic-")
				if e != nil {
					t.Fatal(e)
				}
				defer os.RemoveAll(dir)
				socket := filepath.Join(dir, "s")
				listener, err := net.Listen("unix", socket)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				connected := make(chan net.Conn, 1)
				go func() {
					c, e := listener.Accept()
					if e != nil {
						return
					}
					_, _ = Read(c)
					if partial {
						var h [4]byte
						binary.BigEndian.PutUint32(h[:], 100)
						_, _ = c.Write(append(h[:], '{'))
					}
					connected <- c
				}()
				ctx, cancel := context.WithCancel(context.Background())
				if deadline {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
				}
				defer cancel()
				result := make(chan error, 1)
				go func() {
					_, e := Call(ctx, socket, Envelope{Version: Version, Kind: KindReady, RequestID: "r", Payload: []byte(`{}`)})
					result <- e
				}()
				var c net.Conn
				select {
				case c = <-connected:
					defer c.Close()
				case <-time.After(time.Second):
					t.Fatal("not connected")
				}
				expected := context.DeadlineExceeded
				if !deadline {
					expected = context.Canceled
					cancel()
				}
				select {
				case e := <-result:
					if !errors.Is(e, expected) {
						t.Fatalf("got %v want %v", e, expected)
					}
				case <-time.After(500 * time.Millisecond):
					_ = c.Close()
					<-result
					t.Fatal("connected IO ignored context")
				}
			})
		}
	}
}
