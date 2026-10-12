package host

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestFilterIntegrationUnopenedCarrierSettlement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c := &contract.GitFilterCarrierGrant{ReservationID: "carrier", ScopeSHA256: strings.Repeat("a", 64), FilterReservationID: "filter", ManifestSHA256: strings.Repeat("b", 64), RuntimeNamespace: contract.GitFilterCarrierNamespace}
	f := &filterRuntime{grant: contract.LaunchCommand{GitFilterCarrier: c}}
	if err := f.settleCarrier(ctx); err != nil || f.carrierCleanup == nil || f.carrierCleanup.Outcome != "not_created" {
		t.Fatalf("never opened: %v", err)
	}
	unknown := &filterRuntime{grant: f.grant, carrierAttempted: true}
	if err := unknown.settleCarrier(ctx); err == nil || unknown.carrierCleanup != nil {
		t.Fatal("failed Open reconstructed no-create proof")
	}
}
func TestFilterIntegrationRequiresSignedHelperBeforeInputRead(t *testing.T) {
	h := &Host{}
	if _, err := h.integrateFilteredTarget(context.Background(), nil, adapter.InvocationPayload{}); err == nil || err.Error() != "filter_helper_signed_owner_required" {
		t.Fatalf("missing pin: %v", err)
	}
}
