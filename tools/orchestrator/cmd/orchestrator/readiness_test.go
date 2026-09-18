package main

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

func TestEnsureServerValidatesReadiness(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    ipc.Kind
		payload string
		valid   bool
	}{
		{"error", ipc.KindError, `{"error":"not_ready"}`, false},
		{"wrong-kind", ipc.KindEvent, `{}`, false},
		{"null", ipc.KindResponse, `null`, false},
		{"empty", ipc.KindResponse, `{}`, false},
		{"oversize", ipc.KindResponse, `{}`, false},
		{"wrong-epoch", ipc.KindResponse, `{"status":"ready","epoch":2}`, false},
		{"zero-epoch", ipc.KindResponse, `{"status":"ready","epoch":0}`, false},
		{"wrong-status", ipc.KindResponse, `{"status":"starting","epoch":1}`, false},
		{"ready", ipc.KindResponse, `{"status":"ready","epoch":1}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, e := os.MkdirTemp("", "rd-")
			if e != nil {
				t.Fatal(e)
			}
			defer os.RemoveAll(state)
			l, e := net.Listen("unix", filepath.Join(state, "coordinator.sock"))
			if e != nil {
				t.Fatal(e)
			}
			defer l.Close()
			go func() {
				c, e := l.Accept()
				if e != nil {
					return
				}
				defer c.Close()
				r, e := ipc.Read(c)
				if e != nil {
					return
				}
				if tc.name == "oversize" {
					_ = binary.Write(c, binary.BigEndian, uint32(ipc.MaxMessageSize+1))
					return
				}
				_ = ipc.Write(c, ipc.Envelope{Version: ipc.Version, Kind: tc.kind, RequestID: r.RequestID, Epoch: 1, Payload: []byte(tc.payload)})
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			value, e := ensureServer(ctx, state)
			if tc.valid {
				if e != nil || value["status"] != "ready" || value["epoch"] != uint64(1) {
					t.Fatalf("readiness=%#v err=%v", value, e)
				}
			} else if tc.name == "oversize" {
				if !errors.Is(e, ipc.ErrMessageTooLarge) {
					t.Fatalf("oversize readiness err=%v", e)
				}
			} else if !errors.Is(e, ipc.ErrInvalidMessage) {
				t.Fatalf("invalid readiness=%#v err=%v", value, e)
			}
		})
	}
}

func TestEnsureServerInitialProbeHonorsContext(t *testing.T) {
	state, e := os.MkdirTemp("", "rd-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(state)
	l, e := net.Listen("unix", filepath.Join(state, "coordinator.sock"))
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, e := l.Accept()
		if e == nil {
			accepted <- c
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, e := ensureServer(ctx, state); result <- e }()
	c := <-accepted
	defer c.Close()
	select {
	case e := <-result:
		if !errors.Is(e, context.DeadlineExceeded) {
			t.Fatalf("got %v", e)
		}
	case <-time.After(500 * time.Millisecond):
		_ = c.Close()
		<-result
		t.Fatal("initial ready probe ignored context")
	}
}
