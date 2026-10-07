package coordinator

import (
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

func TestOriginalOwnerPrivateListenerClosesPendingAndRejectsControl(t *testing.T) {
	s, err := NewServer(ownerKeyTestState(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path := s.OwnerSocketPath()
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0600 || info.Mode()&os.ModeSocket == 0 {
		t.Fatal("private owner socket permissions", err)
	}
	control, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	_ = control.SetDeadline(time.Now().Add(time.Second))
	if err = ipc.Write(control, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindReady, RequestID: "not-an-owner-rpc", Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = ipc.Read(control); err == nil {
		t.Fatal("private socket exposed control route")
	}
	_ = control.Close()
	var pending []net.Conn
	defer func() {
		for _, conn := range pending {
			_ = conn.Close()
		}
	}()
	for n := 0; n < 32; n++ {
		conn, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		pending = append(pending, conn)
	}
	deadline := time.Now().Add(time.Second)
	for len(s.ownerAdmission) != 32 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(s.ownerAdmission) != 32 {
		t.Fatalf("pending frames escaped admission: %d", len(s.ownerAdmission))
	}
	extra, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	_ = extra.SetReadDeadline(time.Now().Add(time.Second))
	var byteBuf [1]byte
	if _, err = extra.Read(byteBuf[:]); err == nil {
		t.Fatal("overload was not closed")
	}
	_ = extra.Close()
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked on incomplete owner frames")
	}
	if len(s.ownerAdmission) != 0 {
		t.Fatal("Close returned before all owned RPCs exited")
	}
	if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owner socket survived Close", err)
	}
}
