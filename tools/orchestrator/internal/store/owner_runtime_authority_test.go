package store

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"strings"
	"testing"
)

func TestOriginalOwnerRuntimeBlocksControlAncestry(t *testing.T) {
	f, _, bound := originalOwnerBound(t)
	ctx := context.Background()
	owner := bound.Binding.Owner
	for _, birth := range []string{owner.Birth, "reused-pid-birth"} {
		ancestry := map[int]string{owner.PID: birth}
		want := birth == owner.Birth
		got, err := f.db.IsWorkerAncestry(ctx, ancestry)
		if err != nil || got != want {
			t.Fatalf("public ancestry blocked=%v want=%v err=%v", got, want, err)
		}
		tx, err := f.db.sql.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, err = bridgeWorkerTx(ctx, tx, ancestry)
		_ = tx.Rollback()
		if err != nil || got != want {
			t.Fatalf("bridge TX ancestry blocked=%v want=%v err=%v", got, want, err)
		}
	}
}

func TestOriginalOwnerRuntimeSignedLaunchSizeRollsBackClaim(t *testing.T) {
	ctx := context.Background()
	active := func() (originalOwnerFixture, OriginalOwnerBindResult) {
		f, _, b := originalOwnerBound(t)
		a := appliedFixture(t, f, b)
		if err := f.db.CommitOriginalOwnerApplied(ctx, a, f.key.Public().(ed25519.PublicKey)); err != nil {
			t.Fatal(err)
		}
		if err := f.db.ActivateOriginalOwner(ctx, a, f.key.Public().(ed25519.PublicKey)); err != nil {
			t.Fatal(err)
		}
		return f, b
	}
	prototype, bound := active()
	command, err := prototype.db.ClaimReady(ctx, prototype.prepare.Host.LaunchID, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(command)
	frame, _ := json.Marshal(ipc.Envelope{Version: ipc.Version, Kind: ipc.KindLaunch, RequestID: command.CommandID, Epoch: 1, Payload: body})
	payload, _ := json.Marshal(map[string]string{"pad": strings.Repeat("p", ipc.MaxMessageSize-len(frame)-32)})
	command.AdapterPayload = payload
	if err = validateLaunchSize(command); err != nil {
		t.Fatal("fixture must fit legacy frame", err)
	}
	body, _ = json.Marshal(command)
	if _, err = contract.SignOwnerCommand(contract.OwnerCommand{Version: 1, Permit: bound.Permit, Kind: "launch", RequestID: command.CommandID, Body: body}, prototype.key); err == nil {
		t.Fatal("fixture must exceed signed frame")
	}
	f, _ := active()
	if _, err = f.db.sql.Exec(`UPDATE task_runtime SET adapter_payload=? WHERE host_launch_id=?`, string(payload), f.prepare.Host.LaunchID); err != nil {
		t.Fatal(err)
	}
	before := originalOwnerSnapshot(t, f)
	if _, err = f.db.ClaimReady(ctx, f.prepare.Host.LaunchID, 1, 1); err != CodeError("launch_message_too_large") {
		t.Fatal("untransmittable signed grant committed", err)
	}
	if originalOwnerSnapshot(t, f) != before {
		t.Fatal("oversized signed grant changed durable state")
	}
}

func TestOriginalOwnerRuntimeFenceCannotResurrectReleasedOrForeignHost(t *testing.T) {
	f, _, bound := originalOwnerBound(t)
	ctx := context.Background()
	applied := appliedFixture(t, f, bound)
	if err := f.db.CommitOriginalOwnerApplied(ctx, applied, f.key.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
	if err := f.db.ActivateOriginalOwner(ctx, applied, f.key.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
	foreign := f.prepare.Host
	foreign.Birth = "foreign"
	if err := f.db.FenceOriginalOwnerTransport(ctx, foreign, 1); err != ErrHostRejected {
		t.Fatal("foreign EOF fenced current host", err)
	}
	if err := f.db.FenceOriginalOwnerTransport(ctx, f.prepare.Host, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ClaimReady(ctx, f.prepare.Host.LaunchID, 1, 1); err != ErrHostRejected {
		t.Fatal("fenced host dispatched", err)
	}
	if _, err := f.db.sql.Exec(`UPDATE host_launches SET status='released' WHERE id=?`, f.prepare.Host.LaunchID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.FenceOriginalOwnerTransport(ctx, f.prepare.Host, 1); err != ErrHostRejected {
		t.Fatal("late EOF resurrected released host", err)
	}
	var status string
	if err := f.db.sql.QueryRow(`SELECT status FROM host_launches WHERE id=?`, f.prepare.Host.LaunchID).Scan(&status); err != nil || status != "released" {
		t.Fatal(status, err)
	}
}
