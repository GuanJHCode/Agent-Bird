package gitfilter

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

type gateTestPeer struct{ err error }

func (p gateTestPeer) Verify(context.Context) error { return p.err }

type gateTestOwner struct {
	peer gateLivePeer
	err  error
}

func (o gateTestOwner) bind(context.Context, int) (gateLivePeer, error) { return o.peer, o.err }

func gatePacket(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

func TestOriginalGitGatePendingServeDoesNoCodecIOBeforeActivation(t *testing.T) {
	b, m, permits, journal, owner := exactTestFixture(t)
	session := openExactTest(t, b, m, permits, journal)
	gate, err := NewOriginalGitGate(owner, session, exactHash([]byte("command")))
	if err != nil {
		t.Fatalf("NewOriginalGitGate: %v", err)
	}
	server, client := net.Pipe()
	defer client.Close()
	done := make(chan error, 1)
	go func() { done <- gate.Serve(server, 123) }()
	_ = client.SetReadDeadline(time.Now().Add(25 * time.Millisecond))
	buf := make([]byte, 1)
	if _, err := client.Read(buf); err == nil {
		t.Fatal("pending gate wrote codec bytes before activation")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := gate.Close(closeCtx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := <-done; err == nil {
		t.Fatal("pending serve accepted close as success")
	}
	if b.loads != 0 || b.transforms != 0 || b.reads != 0 {
		t.Fatalf("pending gate touched borrowed session: %+v", b)
	}
}

func TestOriginalGitGateServesOneActivatedV2Request(t *testing.T) {
	b, m, permits, journal, owner := exactTestFixture(t)
	session := openExactTest(t, b, m, permits, journal)
	gate, err := NewOriginalGitGate(owner, session, exactHash([]byte("command")))
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- gate.Serve(server, 17) }()
	if err := gate.activateWith(owner, gateTestOwner{peer: gateTestPeer{}}, exactHash([]byte("command"))); err != nil {
		t.Fatal(err)
	}
	in := gatePacket("git-filter-client\n") + gatePacket("version=2\n") + "0000" + gatePacket("capability=smudge\n") + "0000" + gatePacket("command=smudge\n") + gatePacket("pathname=a.txt\n") + "0000" + gatePacket("B") + "0000"
	written := make(chan error, 1)
	go func() { _, e := io.WriteString(client, in); written <- e }()
	want := gatePacket("git-filter-server\n") + gatePacket("version=2\n") + "0000" + gatePacket("capability=smudge\n") + "0000" + gatePacket("status=success\n") + "0000" + gatePacket(string([]byte{0x18})) + "0000" + "0000"
	got := make([]byte, len(want))
	if _, err := io.ReadFull(client, got); err != nil || string(got) != want {
		t.Fatalf("response=%q err=%v", got, err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	state := gate.State()
	if !state.Drained || state.Calls != 1 || state.ResponsesCommitted != 1 || b.closes != 0 || b.cleanups != 0 {
		t.Fatalf("state=%+v broker=%+v", state, b)
	}
}

func TestOriginalGitGateRejectsZeroSessionAndDuplicateActivation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if gate, err := NewOriginalGitGate(ctx, new(ExactTransformSession), exactHash([]byte("command"))); err == nil || gate != nil {
		t.Fatalf("zero session gate=%v err=%v", gate, err)
	}
	b, m, permits, journal, owner := exactTestFixture(t)
	gate, err := NewOriginalGitGate(owner, openExactTest(t, b, m, permits, journal), exactHash([]byte("command")))
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.activateWith(owner, gateTestOwner{peer: gateTestPeer{}}, exactHash([]byte("command"))); err != nil {
		t.Fatal(err)
	}
	if err := gate.activateWith(owner, gateTestOwner{peer: gateTestPeer{}}, exactHash([]byte("command"))); err == nil || !gate.State().Revoked || !gate.State().Failed {
		t.Fatalf("duplicate activation state=%+v err=%v", gate.State(), err)
	}
}
