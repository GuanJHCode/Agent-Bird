package store

import (
	"encoding/json"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

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
