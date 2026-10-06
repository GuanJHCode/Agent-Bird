package main

import (
	"context"
	"flag"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"io"
)

type resumeContinuationFile struct {
	store.ResumeContinuationSpec
	ControlFile string `json:"control_file"`
}

func resumeContinuation(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("resume_continuation", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "private state directory")
	path := fs.String("request", "", "owner-only observed-source decision")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *path == "" {
		return codeError("invalid_args")
	}
	var request resumeContinuationFile
	if err := readPrivateJSON(*path, &request); err != nil {
		return err
	}
	if request.ControlFile == "" || request.TaskID == "" {
		return codeError("invalid_continuation")
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	cap, err := loadControlCapability(request.ControlFile)
	if err != nil {
		return err
	}
	if err = ensureSourceHost(ctx, state, cap); err != nil {
		return err
	}
	response, err := call(ctx, state, ipc.KindResumeContinuation, coordinator.ResumeContinuationRequest{ResumeContinuationSpec: request.ResumeContinuationSpec, ControllerThread: cap.ControllerThread, ControlToken: cap.ControlToken})
	if err != nil {
		return err
	}
	_, err = out.Write(append(response.Payload, '\n'))
	return err
}
