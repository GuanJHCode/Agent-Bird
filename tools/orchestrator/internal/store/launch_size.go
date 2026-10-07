package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

// Check the actual transport format before committing a claim or its replay.
// A valid legacy frame can exceed the cap once the exact owner permit and
// signature are included; failing at send time would strand a reservation.
func validateHostLaunchSize(ctx context.Context, tx *sql.Tx, command contract.LaunchCommand) error {
	if err := validateLaunchSize(command); err != nil {
		return err
	}
	var mode string
	if err := tx.QueryRowContext(ctx, `SELECT execution_owner_mode FROM host_launches WHERE host_id=?`, command.HostID).Scan(&mode); err != nil {
		return err
	}
	if mode == "legacy" {
		return nil
	}
	if mode != "supervised-v1" {
		return ErrHostRejected
	}
	var permitJSON, hash string
	err := tx.QueryRowContext(ctx, `SELECT c.permit_json,c.permit_hash FROM host_launches l JOIN execution_owners o ON o.launch_id=l.id JOIN owner_control_changes c ON c.owner_id=o.owner_id AND c.revision=o.control_revision WHERE l.host_id=? AND o.is_current=1 AND o.phase='bound' AND o.applied_control_revision=o.control_revision AND c.coordinator_epoch=?`, command.HostID, command.ExecutionEpoch).Scan(&permitJSON, &hash)
	if err != nil || originalOwnerHash([]byte(permitJSON)) != hash {
		return ErrHostRejected
	}
	permit, err := contract.DecodeOwnerControlPermit([]byte(permitJSON))
	if err != nil {
		return err
	}
	body, err := json.Marshal(command)
	if err != nil {
		return err
	}
	owner := contract.OwnerCommand{Version: 1, Permit: permit, Kind: "launch", RequestID: command.CommandID, BodySHA256: originalOwnerHash(body), Body: body, Signature: strings.Repeat("0", 128)}
	body, err = json.Marshal(owner)
	if err != nil {
		return err
	}
	if len(body) > contract.OriginalOwnerWireLimit {
		return CodeError("launch_message_too_large")
	}
	frame, err := json.Marshal(ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerCommand, RequestID: command.CommandID, Epoch: command.ExecutionEpoch, Payload: body})
	if err != nil {
		return err
	}
	if len(frame) > ipc.MaxMessageSize {
		return CodeError("launch_message_too_large")
	}
	return nil
}

func validateLaunchSize(command contract.LaunchCommand) error {
	body, err := json.Marshal(command)
	if err != nil {
		return err
	}
	message, err := json.Marshal(ipc.Envelope{Version: ipc.Version, Kind: ipc.KindLaunch, RequestID: command.CommandID, Epoch: command.ExecutionEpoch, Payload: body})
	if err != nil {
		return err
	}
	if len(message) > ipc.MaxMessageSize {
		return CodeError("launch_message_too_large")
	}
	return nil
}
