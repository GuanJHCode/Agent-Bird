package store

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type acpStoreFixture struct {
	bridgeDeliveryFixture
	bind      contract.ACPBindRequest
	view      contract.BridgeBindingView
	stopChild func()
}

func newACPStoreFixture(t *testing.T, activate bool) acpStoreFixture {
	t.Helper()
	f := acpStoreFixture{bridgeDeliveryFixture: newBridgeDeliveryFixture(t)}
	if _, err := f.db.sql.Exec(`DELETE FROM bridge_bindings WHERE binding_id=?`, f.binding); err != nil {
		t.Fatal(err)
	}
	child := exec.Command("/bin/cat")
	in, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	var stopOnce sync.Once
	f.stopChild = func() {
		stopOnce.Do(func() {
			_ = in.Close()
			done := make(chan error, 1)
			go func() { done <- child.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("original child Wait: %v", err)
				}
			case <-time.After(3 * time.Second):
				_ = child.Process.Kill()
				<-done
				t.Error("owned child did not exit after EOF")
			}
		})
	}
	t.Cleanup(f.stopChild)
	birth, err := process.Birth(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := process.ExecutablePath(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := process.ExecutableDigest(context.Background(), exe)
	if err != nil {
		t.Fatal(err)
	}
	f.bind = contract.ACPBindRequest{SchemaVersion: 1, RequestID: "bind-1", RunID: "run", ProviderID: "grok", NativePID: child.Process.Pid, NativeBirth: birth, NativeExecutableSHA256: digest, TransportMode: contract.BridgeTransportGrokACP, OwnerMode: "local", OwnerCapabilityFile: "/private/owner.json", ControlFile: "/private/control.json", ManifestSHA256: contract.GrokACPManifestSHA256()}
	if activate {
		f.view, err = f.db.BindACPBridge(f.owner, f.bind)
		if err != nil {
			t.Fatal(err)
		}
		f.binding = f.view.BindingID
		r := contract.ACPActivateRequest{BridgeOwnerRequest: f.ownerRequest(""), NativeSessionID: "session-one"}
		r.RequestID = contract.ACPActivateRequestID(r)
		f.view, err = f.db.ActivateACPBridge(f.owner, r)
		if err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f acpStoreFixture) beginRequest(claim contract.ACPClaimReply) contract.ACPBeginRequest {
	r := contract.ACPBeginRequest{BridgeOwnerRequest: f.ownerRequest(""), NativeSessionID: claim.NativeSessionID, BatchID: claim.Batch.BatchID, AttemptID: claim.Batch.AttemptID, ClaimToken: claim.ClaimToken, RenderSHA256: claim.Batch.RenderSHA256, PromptSHA256: claim.PromptSHA256, TurnOrdinal: claim.TurnOrdinal}
	r.RequestID = contract.ACPBeginRequestID(r)
	return r
}

func acpReceiveRequest(begin contract.ACPBeginRequest, rpcID int64) contract.ACPReceiveRequest {
	r := contract.ACPReceiveRequest{ACPBeginRequest: begin, RPCRequestID: rpcID, ResponseSHA256: strings.Repeat("d", 64), StopReason: "end_turn"}
	r.RequestID = contract.ACPReceiveRequestID(r)
	return r
}

func TestACPStoreBeginReceiveAndCommittedReplayAfterChildExit(t *testing.T) {
	f := newACPStoreFixture(t, true)
	before := bridgeBusinessSnapshot(t, f.db)
	claim, err := f.db.ClaimACPBridge(f.owner, contract.ACPClaimRequest{BridgeOwnerRequest: f.ownerRequest("claim")})
	if err != nil {
		t.Fatal(err)
	}
	begin := f.beginRequest(claim)
	receive := acpReceiveRequest(begin, 4)
	if _, err := f.db.ReceiveACPBridge(f.owner, receive); err == nil {
		t.Fatal("CLAIMED directly received")
	}
	if err := f.db.BeginACPBridgeEmit(f.owner, begin); err != nil {
		t.Fatal(err)
	}
	if err := f.db.BeginACPBridgeEmit(f.owner, begin); err != nil {
		t.Fatalf("lost Begin ACK replay: %v", err)
	}
	var state, session string
	var ordinal int64
	if err := f.db.sql.QueryRow(`SELECT state,acp_session_id,acp_turn_ordinal FROM bridge_delivery_attempts WHERE attempt_id=?`, begin.AttemptID).Scan(&state, &session, &ordinal); err != nil || state != "EMITTING" || session != begin.NativeSessionID || ordinal != 1 {
		t.Fatalf("begin state=%s session=%s ordinal=%d err=%v", state, session, ordinal, err)
	}
	receipt, err := f.db.ReceiveACPBridge(f.owner, receive)
	if err != nil {
		t.Fatal(err)
	}
	f.stopChild()
	replay, err := f.db.ReceiveACPBridge(f.owner, receive)
	if err != nil || replay != receipt {
		t.Fatalf("committed ACK-loss replay=%+v err=%v", replay, err)
	}
	changed := receive
	changed.ResponseSHA256 = strings.Repeat("e", 64)
	changed.RequestID = contract.ACPReceiveRequestID(changed)
	if _, err := f.db.ReceiveACPBridge(f.owner, changed); err == nil {
		t.Fatal("different response replaced committed receipt")
	}
	if after := bridgeBusinessSnapshot(t, f.db); after != before {
		t.Fatal("protocol receive changed business acceptance")
	}
}

func TestACPStoreUncertainOrDeadChildCannotCommitFirstReceipt(t *testing.T) {
	for _, mode := range []string{"recovered-uncertain", "dead-child"} {
		t.Run(mode, func(t *testing.T) {
			f := newACPStoreFixture(t, true)
			claim, err := f.db.ClaimACPBridge(f.owner, contract.ACPClaimRequest{BridgeOwnerRequest: f.ownerRequest("claim")})
			if err != nil {
				t.Fatal(err)
			}
			begin := f.beginRequest(claim)
			if err := f.db.BeginACPBridgeEmit(f.owner, begin); err != nil {
				t.Fatal(err)
			}
			if mode == "recovered-uncertain" {
				if err := f.db.RecoverBridgeDelivery(f.owner); err != nil {
					t.Fatal(err)
				}
			} else {
				f.stopChild()
			}
			if _, err := f.db.ReceiveACPBridge(f.owner, acpReceiveRequest(begin, 4)); err == nil {
				t.Fatal("uncommitted receipt settled without live emitting identity")
			}
			if err := f.db.BeginACPBridgeEmit(f.owner, begin); err == nil {
				t.Fatal("uncertain/dead Begin replay authorized another send")
			}
			if _, err := f.db.ClaimACPBridge(f.owner, contract.ACPClaimRequest{BridgeOwnerRequest: f.ownerRequest("next-claim")}); err == nil {
				t.Fatal("uncertain/dead attempt admitted new claim")
			}
		})
	}
}

func (f acpStoreFixture) ownerRequest(id string) contract.BridgeOwnerRequest {
	return contract.BridgeOwnerRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: f.binding, OwnerEpoch: 1, RequestID: id}, OwnerMode: "local", OwnerCapabilityFile: "/private/owner.json", ControlFile: "/private/control.json"}
}

func TestACPStoreBindAndActivateUseStrictOwnerContract(t *testing.T) {
	f := newACPStoreFixture(t, false)
	bad := f.bind
	bad.RequestID = ""
	if _, err := f.db.BindACPBridge(f.owner, bad); err == nil {
		t.Fatal("empty bind RID admitted")
	}
	view, err := f.db.BindACPBridge(f.owner, f.bind)
	if err != nil {
		t.Fatal(err)
	}
	f.binding = view.BindingID
	if view.CapabilityFile != "" || view.Status != "PENDING_SESSION" {
		t.Fatalf("binding=%+v", view)
	}
	if _, err := os.Stat(BridgeCapabilityPath(strings.TrimSuffix(f.db.path, "/state.db"), "run")); !os.IsNotExist(err) {
		t.Fatalf("ACP minted Hook file: %v", err)
	}
	if replay, err := f.db.BindACPBridge(f.owner, f.bind); err != nil || replay != view {
		t.Fatalf("bind replay=%+v err=%v", replay, err)
	}
	r := contract.ACPActivateRequest{BridgeOwnerRequest: f.ownerRequest(""), NativeSessionID: ""}
	r.RequestID = contract.ACPActivateRequestID(r)
	if _, err := f.db.ActivateACPBridge(f.owner, r); err == nil {
		t.Fatal("empty ACP session admitted")
	}
	r.NativeSessionID = "session-one"
	r.RequestID = contract.ACPActivateRequestID(r)
	view, err = f.db.ActivateACPBridge(f.owner, r)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := f.db.ActivateACPBridge(f.owner, r); err != nil || replay != view {
		t.Fatalf("activate replay=%+v err=%v", replay, err)
	}
	r.NativeSessionID = "session-other"
	r.RequestID = contract.ACPActivateRequestID(r)
	if _, err := f.db.ActivateACPBridge(f.owner, r); err == nil {
		t.Fatal("second ACP session admitted")
	}
}

func TestACPStoreClaimReplaysCanonicalBatchAndPersistsCollectionProof(t *testing.T) {
	f := newACPStoreFixture(t, true)
	before := bridgeBusinessSnapshot(t, f.db)
	r := contract.ACPClaimRequest{BridgeOwnerRequest: f.ownerRequest("claim-one")}
	first, err := f.db.ClaimACPBridge(f.owner, r)
	if err != nil {
		t.Fatal(err)
	}
	if first.TurnOrdinal != 1 || first.NativeSessionID != "session-one" || first.PromptSHA256 != bridgeSHA([]byte(first.PromptText)) {
		t.Errorf("incomplete claim: %+v", first)
	}
	second, err := f.db.ClaimACPBridge(f.owner, r)
	if err != nil {
		t.Errorf("ACK-loss replay: %v", err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Error("claim replay changed batch/token/prompt/ordinal")
	}
	var count int
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM collection_receipts`).Scan(&count); err != nil || count != 1 {
		t.Errorf("collection proof count=%d err=%v", count, err)
	}
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM bridge_delivery_attempts`).Scan(&count); err != nil || count != 1 {
		t.Errorf("attempt count=%d err=%v", count, err)
	}
	if after := bridgeBusinessSnapshot(t, f.db); after != before {
		t.Error("claim changed business state")
	}
}

func TestACPStoreExpiredClaimIsNoSendAndReusesUnallocatedOrdinal(t *testing.T) {
	f := newACPStoreFixture(t, true)
	r := contract.ACPClaimRequest{BridgeOwnerRequest: f.ownerRequest("claim-one")}
	first, err := f.db.ClaimACPBridge(f.owner, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.sql.Exec(`UPDATE bridge_delivery_attempts SET deadline_at=? WHERE attempt_id=?`, bridgeTime(time.Now().Add(-time.Second)), first.Batch.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ClaimACPBridge(f.owner, r); err == nil || err.Error() != "bridge_claim_expired_no_send" {
		t.Errorf("old expired RID = %v", err)
	}
	r.RequestID = "claim-two"
	second, err := f.db.ClaimACPBridge(f.owner, r)
	if err != nil {
		t.Fatal(err)
	}
	if second.Batch.BatchID != first.Batch.BatchID || second.Batch.AttemptID == first.Batch.AttemptID || second.TurnOrdinal != 1 {
		t.Fatalf("no-send retry=%+v", second)
	}
	var allocated int
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM bridge_delivery_attempts WHERE acp_turn_ordinal IS NOT NULL`).Scan(&allocated); err != nil || allocated != 0 {
		t.Fatalf("claim allocated turn before Begin: %d %v", allocated, err)
	}
}

func TestACPStoreHookOwnerEntriesRejectACPTransport(t *testing.T) {
	f := newACPStoreFixture(t, true)
	owner := f.ownerRequest("hook-request")
	if _, err := f.db.BridgeStatus(f.owner, contract.BridgeStatusRequest{BridgeOwnerRequest: owner}); err == nil {
		t.Error("Hook status admitted ACP")
	}
	if err := f.db.UnbindBridge(f.owner, contract.BridgeUnbindRequest{BridgeOwnerRequest: owner}); err == nil {
		t.Error("Hook unbind mutated ACP")
	}
	view, err := f.db.BridgeBindingIdentity(f.owner, f.binding)
	if err != nil || view.Status != "ACTIVE" {
		t.Fatalf("ACP binding changed: %+v err=%v", view, err)
	}
}
