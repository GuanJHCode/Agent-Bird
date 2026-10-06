package host

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Actual Store SubmitPlan/ClaimReady grant -> private Host preparation. Only
// owned synthetic configuration/auth metadata; no process/exit capability.
func TestCodexCanonicalStoreGrantEntersActualHostPreparation(t *testing.T) {
	h, fixtureGrant, inv, env := codexOriginFixture(t)
	payload, err := adapter.DecodeInvocationPayload(fixtureGrant.AdapterPayload)
	if err != nil {
		t.Fatal(err)
	}
	payload.BinaryPath, payload.BinaryVersion, payload.BinarySHA256 = "", "", ""
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	receipt, err := db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "store-run", ControllerThread: "controller", PlanRevision: 1, OriginContextID: "owner", OriginPID: 1, OriginBirth: "synthetic-owner"}, Host: store.HostLaunchSpec{OriginContextID: "owner", HostGeneration: "generation", Executable: "/private/fixture/orchestrator"}, Tasks: []store.TaskSpec{{ID: "store-task", RunID: "store-run", MaxAttempts: 1, MaxActiveMS: 60000, AdapterPayload: raw}}})
	if err != nil {
		t.Fatal(err)
	}
	producer, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "owner", OriginPID: 1, OriginBirth: "synthetic-owner", HostGeneration: "generation", PID: 10, Birth: "synthetic-host", Executable: "/private/fixture/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, producer, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if string(grant.AdapterPayload) != string(raw) || grant.SessionID != "" || grant.Continuation != nil {
		t.Fatal("Store changed immutable initial payload")
	}
	h, err = NewIPC(h.spoolRoot, producer)
	if err != nil || h.BindContinuationOwner("owner") != nil {
		t.Fatal("Host owner")
	}
	if _, err := h.spool(store.Attempt{ID: grant.AttemptID, SegmentID: grant.SegmentID}); err != nil {
		t.Fatal(err)
	}
	prepared, err := h.prepareManagedCodexState(ctx, grant, inv, env, codexPreparationControl{})
	if err != nil {
		t.Fatal("actual canonical Store grant cannot enter Host preparation", err)
	}
	if prepared.Origin.Scope.RunID != grant.RunID || prepared.Origin.Scope.AttemptID != grant.AttemptID || prepared.Origin.Scope.AdapterPayloadSHA256 != codexDigest(raw) || prepared.Origin.Lock != *payload.ProviderLock || len(h.codexLeases) != 0 {
		t.Fatal("canonical combined admission lost lock/scope or fabricated process")
	}
}

func TestCodexCanonicalPayloadRejectsLegacyBinaryShadows(t *testing.T) {
	for _, field := range []string{"path", "version", "hash", "all"} {
		t.Run(field, func(t *testing.T) {
			h, g, inv, env := codexOriginFixture(t)
			payload, err := adapter.DecodeInvocationPayload(g.AdapterPayload)
			if err != nil {
				t.Fatal(err)
			}
			payload.BinaryPath, payload.BinaryVersion, payload.BinarySHA256 = "", "", ""
			switch field {
			case "path":
				payload.BinaryPath = payload.ProviderLock.Binary.Path
			case "version":
				payload.BinaryVersion = payload.ProviderLock.Binary.Version
			case "hash":
				payload.BinarySHA256 = payload.ProviderLock.Binary.SHA256
			case "all":
				payload.BinaryPath, payload.BinaryVersion, payload.BinarySHA256 = payload.ProviderLock.Binary.Path, payload.ProviderLock.Binary.Version, payload.ProviderLock.Binary.SHA256
			}
			g.AdapterPayload, err = json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{}); err == nil {
				t.Fatal("legacy binary shadow became authority")
			}
		})
	}
}
