package coordinator

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestOriginalOwnerRejectedReplacementLeavesDurableStateUntouched(t *testing.T) {
	s, hello := ownerRuntimeFixture(t)
	ctx := context.Background()
	// This controlled same-executable child is only an old physical transport
	// identity. The Store B1 setup below is a protocol fixture, not owner RPC.
	step, finish := startOwnerRPCChild(t, ownerChildConfig{})
	identity := step(ownerChildStep{Kind: "identity"}).Owner
	old := hello
	old.PID, old.Birth = identity.PID, identity.Birth
	hostID, err := s.db.RegisterHost(ctx, old, s.epoch)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := process.ExecutableDigest(ctx, hello.Executable)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := s.db.PreviewOriginalOwnerScope(ctx, old, s.epoch, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ownerKeys.saveLaunchCredential(s.stateDir, scope, hello.LaunchToken); err != nil {
		t.Fatal(err)
	}
	bootstrap := strings.Repeat("a", 64)
	hash, _ := contract.OriginalOwnerBootstrapSHA256(bootstrap)
	intent, err := s.db.PrepareOriginalOwner(ctx, store.PrepareOriginalOwnerSpec{Host: old, Epoch: s.epoch, ExecutableSHA256: digest, BootstrapID: "old-bootstrap", BootstrapTokenSHA256: hash, CoordinatorKeyID: s.ownerKeys.id, RequestID: "old-intent", RequestSHA256: strings.Repeat("b", 64), StartupDeadlineUnixMS: time.Now().Add(20 * time.Second).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed([]byte(strings.Repeat("o", 32)))
	owner := contract.OriginalOwnerIdentity{PID: identity.PID + 1000000, Birth: "store-fixture-owner", PublicKey: hex.EncodeToString(key.Public().(ed25519.PublicKey))}
	auth := store.OriginalOwnerAuthentication{Owner: owner, OriginPID: hello.OriginPID, OriginBirth: hello.OriginBirth, ExecutableSHA256: digest}
	bound, err := s.db.BindOriginalOwner(ctx, store.BindOriginalOwnerSpec{Hello: contract.OriginalOwnerHello{Scope: intent.Scope, BootstrapID: intent.BootstrapID, BootstrapToken: bootstrap, Owner: owner, FirstHost: intent.FirstHost, CoordinatorKeyID: s.ownerKeys.id}, Host: old, Epoch: s.epoch, Authenticated: auth}, s.ownerKeys.sign)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := contract.SignOwnerPermitApplied(contract.OwnerPermitApplied{Permit: bound.Permit, Reconciled: contract.HostReconciled{ProducerID: hostID}}, key)
	if err != nil {
		t.Fatal(err)
	}
	spec := store.OriginalOwnerAppliedSpec{Applied: applied, Host: old, Epoch: s.epoch, Authentication: auth}
	if err = s.db.CommitOriginalOwnerApplied(ctx, spec, s.ownerPublicKey()); err != nil {
		t.Fatal(err)
	}
	if err = s.db.ActivateOriginalOwner(ctx, spec, s.ownerPublicKey()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ClaimReady(ctx, hostID, s.epoch, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.db.MarkHostOffline(ctx, hostID, s.epoch); err != nil {
		t.Fatal(err)
	}
	finish()
	if dead, err := hostIdentityExited(old.PID, old.Birth); err != nil || !dead {
		t.Fatal("old transport death not positively proven", err)
	}
	before := hostHelloDBSnapshot(t, s)
	conn, err := net.Dial("unix", s.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	body, _ := json.Marshal(hello)
	if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostHello, RequestID: "unauthorized-replacement", Payload: body}); err != nil {
		t.Fatal(err)
	}
	reply, err := ipc.Read(conn)
	if err != nil || reply.Kind != ipc.KindError {
		t.Fatal("replacement not rejected", err)
	}
	var rejected ErrorResponse
	if err = json.Unmarshal(reply.Payload, &rejected); err != nil || rejected.Error != string(store.ErrHostRejected) {
		t.Fatal("unexpected rejection", rejected, err)
	}
	if after := hostHelloDBSnapshot(t, s); after != before {
		t.Fatal("rejected supervised replacement changed durable runtime/launch/segment state")
	}
}
