package host

import (
	"context"
	"reflect"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
)

type codexPhysicalState struct {
	Primary          codexPrimaryInventory `json:"primary"`
	Slots            []codexPersistentSlot `json:"slots"`
	Auxiliary        codexAuxiliaryState   `json:"auxiliary"`
	Bytes            int64                 `json:"bytes"`
	RequiredAbsences []string              `json:"required_absences"`
}

type codexPhysicalObservation struct {
	State     codexPhysicalState
	Layout    codexStateLayout
	Binding   codexsession.ThreadBinding
	Auxiliary codexAuxiliaryObservation
}

// Runtime byte material only. The protected/source reference barrier and real
// original process/Session/Question evidence remain separate mandatory inputs
// to the later protected seal publisher; this function publishes nothing.
func codexCapturePhysicalState(ctx context.Context, layout codexStateLayout, binding codexsession.ThreadBinding, installationID string, read codexStateReadControl) (codexPhysicalState, error) {
	return codexCapturePhysicalObserved(ctx, layout, binding, installationID, read, nil)
}

func codexCapturePhysicalObserved(ctx context.Context, layout codexStateLayout, binding codexsession.ThreadBinding, installationID string, read codexStateReadControl, retained *codexPhysicalObservation) (codexPhysicalState, error) {
	var state codexPhysicalState
	if read.Remaining == nil || *read.Remaining < 0 || *read.Remaining > codexStateTotalLimit {
		return state, errCodexStateFile
	}
	before := *read.Remaining
	var err error
	state.Primary, err = codexCapturePrimaryInventory(ctx, layout, &binding, codexPrimaryControl{})
	if err != nil {
		return codexPhysicalState{}, err
	}
	state.Slots, err = codexCapturePersistentSlots(ctx, layout.Home, layout.SQLite, binding, installationID, read)
	if err != nil {
		return codexPhysicalState{}, err
	}
	var auxiliary codexAuxiliaryObservation
	state.Auxiliary, err = codexCaptureAuxiliaryObserved(ctx, layout.Auxiliary, codexAuxiliaryControl{Read: read}, &auxiliary)
	if err != nil {
		return codexPhysicalState{}, err
	}
	state.Bytes = before - *read.Remaining
	state.RequiredAbsences = codexRequiredAbsences()
	observation := codexPhysicalObservation{State: state, Layout: layout, Binding: binding, Auxiliary: auxiliary}
	if codexVerifyPhysicalObservation(ctx, observation) != nil {
		return codexPhysicalState{}, errCodexStateFile
	}
	if retained != nil {
		*retained = observation
	}
	return state, nil
}

// The later publisher retains this observation until its protected/source
// checks also finish, then joins both metadata barriers immediately before
// publication. No content is reread and no read budget is created here.
func codexVerifyPhysicalObservation(ctx context.Context, observed codexPhysicalObservation) error {
	if !reflect.DeepEqual(observed.State.RequiredAbsences, codexRequiredAbsences()) || ctx.Err() != nil || len(observed.State.Slots) != 20 || observed.State.Bytes < 0 || observed.State.Bytes > codexStateTotalLimit {
		return errCodexStateFile
	}
	primary, err := codexCapturePrimaryInventory(ctx, observed.Layout, &observed.Binding, codexPrimaryControl{})
	if err != nil || !reflect.DeepEqual(primary, observed.State.Primary) {
		return errCodexStateFile
	}
	parents := map[string]codexStateDirectory{}
	for _, parent := range observed.State.Primary.Parents {
		parents[parent.Path] = parent
	}
	for _, slot := range observed.State.Slots {
		root, relative := observed.Layout.Home, strings.TrimPrefix(slot.Name, "home/")
		if strings.HasPrefix(slot.Name, "sqlite/") {
			root, relative = observed.Layout.SQLite, strings.TrimPrefix(slot.Name, "sqlite/")
		} else if !strings.HasPrefix(slot.Name, "home/") {
			return errCodexStateFile
		}
		if codexCheckPersistentSlotMetadata(ctx, root, relative, slot.File, parents) != nil {
			return errCodexStateFile
		}
	}
	if codexVerifyAuxiliaryObservation(ctx, observed.Auxiliary) != nil {
		return errCodexStateFile
	}
	if ctx.Err() != nil {
		return errCodexStateFile
	}
	return nil
}
