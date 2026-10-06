package coordinator

import (
	"encoding/json"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

// This owned UDS relay is only a wire barrier around the existing fixtures.
// Peers remain the same test process, so HostHello OS authentication is real.
type c02WireBarrier struct {
	socket string
	lost   chan struct{}
	once   sync.Once
}

func c02StartWireBarrier(t *testing.T, root, target string, dropExitedACK bool) *c02WireBarrier {
	t.Helper()
	p := &c02WireBarrier{socket: filepath.Join(root, "wire.sock"), lost: make(chan struct{})}
	listener, err := net.Listen("unix", p.socket)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	connections := make(map[net.Conn]bool)
	acceptedDone := make(chan struct{})
	var workers sync.WaitGroup
	go func() {
		defer close(acceptedDone)
		for {
			source, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections[source] = true
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { _ = source.Close(); mu.Lock(); delete(connections, source); mu.Unlock() }()
				upstream, err := net.DialTimeout("unix", target, time.Second)
				if err != nil {
					return
				}
				defer upstream.Close()
				var exited atomic.Value
				exited.Store("")
				done := make(chan struct{}, 2)
				relay := func(from, to net.Conn, outbound bool) {
					defer func() { _ = source.Close(); _ = upstream.Close(); done <- struct{}{} }()
					for {
						frame, err := ipc.Read(from)
						if err != nil {
							return
						}
						if outbound && frame.Kind == ipc.KindEvent {
							var event contract.Event
							if json.Unmarshal(frame.Payload, &event) == nil && event.Kind == contract.EventExited {
								exited.Store(frame.RequestID)
							}
						}
						drop := !outbound && ((dropExitedACK && frame.Kind == ipc.KindDurableAck && frame.RequestID == exited.Load().(string) && frame.RequestID != "") || (!dropExitedACK && frame.Kind == ipc.KindHostDisposition))
						if drop {
							didDrop := false
							p.once.Do(func() { didDrop = true; close(p.lost) })
							if didDrop {
								return
							}
						}
						_ = to.SetWriteDeadline(time.Now().Add(2 * time.Second))
						if ipc.Write(to, frame) != nil {
							return
						}
					}
				}
				go relay(source, upstream, true)
				go relay(upstream, source, false)
				<-done
				<-done
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-acceptedDone
		mu.Lock()
		for conn := range connections {
			_ = conn.Close()
		}
		mu.Unlock()
		done := make(chan struct{})
		go func() { workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("wire barrier retained an owned relay")
		}
	})
	return p
}
