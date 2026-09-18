package main

import (
	"context"
	"flag"
	"io"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type reworkFile struct {
	store.ReworkSpec
	ControlFile string `json:"control_file"`
}

func reworkTask(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("rework", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "private state directory")
	path := fs.String("request", "", "owner-only rework request")
	if fs.Parse(args) != nil || *path == "" || fs.NArg() != 0 {
		return codeError("invalid_args")
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	var request reworkFile
	if err = readPrivateJSON(*path, &request); err != nil || request.TaskID == "" || request.ControlFile == "" {
		return codeError("invalid_rework")
	}
	capability, err := loadControlCapability(request.ControlFile)
	if err != nil {
		return err
	}
	// Rework requires the still-ready original Host. It does not implicitly
	// recover a retired, interrupted or unknown runtime.
	response, err := call(ctx, state, ipc.KindRework, coordinator.ReworkRequest{ReworkSpec: request.ReworkSpec, ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken})
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(response.Payload, '\n'))
	return err
}
