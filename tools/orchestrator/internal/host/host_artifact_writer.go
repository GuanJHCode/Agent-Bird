package host

import (
	"context"
	"encoding/json"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Freeze the artifact permission before invoking preparation/provider callbacks.
// A copied LaunchCommand alone would retain the caller's mutable grant pointer.
func metadataForLaunch(g contract.LaunchCommand) launchMetadata {
	meta := launchMetadata{commandID: g.CommandID, workRevision: g.WorkRevision, executionEpoch: g.ExecutionEpoch, questionRevision: g.QuestionRevision}
	if g.HostArtifacts != nil {
		permission := *g.HostArtifacts
		g.HostArtifacts = &permission
		meta.artifactGrant = &g
	}
	if g.Diagnostics != nil {
		permission := *g.Diagnostics
		copy := g
		copy.Diagnostics = &permission
		meta.diagnosticsGrant = &copy
	}
	return meta
}

func (h *Host) artifactSlot(meta launchMetadata, class string, index int) (artifacts.HostSlotBinding, error) {
	if meta.artifactGrant == nil {
		return artifacts.HostSlotBinding{}, contract.ErrHostArtifactGrant
	}
	g := *meta.artifactGrant
	if err := contract.RequireHostArtifacts(g); err != nil {
		return artifacts.HostSlotBinding{}, err
	}
	if g.HostID != h.producerID || meta.commandID != g.CommandID || meta.workRevision != g.WorkRevision {
		return artifacts.HostSlotBinding{}, contract.ErrHostArtifactGrant
	}
	reservation := g.HostArtifacts.TerminalReservationID
	if class == "progress" {
		reservation = g.HostArtifacts.ProgressReservationID
	}
	b := artifacts.HostSlotBinding{Version: 1, ReservationID: reservation, ScopeSHA256: g.HostArtifacts.ScopeSHA256, Class: class, Index: index}
	return b, b.Validate()
}

func hostArtifactReference(id string, d artifacts.HostSlotDescriptor, p artifacts.Proof) (*contract.ArtifactRef, error) {
	descriptor, err := d.Canonical()
	if err != nil {
		return nil, err
	}
	proof, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	receipt := &contract.HostArtifactReceipt{Version: 1, Descriptor: descriptor, Proof: proof}
	if err = receipt.Validate(); err != nil {
		return nil, err
	}
	return &contract.ArtifactRef{ID: id, Path: p.Path, Size: p.Size, SHA256: p.SHA256, HostArtifact: receipt}, nil
}

func (h *Host) writeHostArtifact(meta launchMetadata, class string, index int, kind, id string, body []byte) (*contract.ArtifactRef, error) {
	binding, err := h.artifactSlot(meta, class, index)
	if err != nil {
		return nil, err
	}
	d := artifacts.HostSlotDescriptor{HostSlotBinding: binding, Kind: kind, Size: int64(len(body)), SHA256: hashBytes(body)}
	// Terminal/prelaunch publication is needed even after the execution context
	// was canceled. Bound waiting on another writer without reviving execution.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p, err := artifacts.MaterializeHostSlot(ctx, h.spoolRoot, d, body)
	if err != nil {
		return nil, err
	}
	return hostArtifactReference(id, d, p)
}
