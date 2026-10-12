package main

import (
	"context"
	"flag"
	"io"
	"os/signal"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

// This private entry accepts only the dedicated launcher's inherited channels.
// It has no credential flags, discovery files or Coordinator Ensure callback.
func ownerTransportEntry(parent context.Context, args []string) error {
	fs := flag.NewFlagSet("owner-transport", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	bootstrapFD := fs.Int("bootstrap-fd", -1, "inherited public bootstrap")
	controlFD := fs.Int("control-fd", -1, "inherited owner control")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *bootstrapFD != 3 || *controlFD != 4 {
		return codeError("invalid_args")
	}
	ctx, cancel := signal.NotifyContext(parent, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	control, err := ipc.TakeOwnerControlFD(*controlFD)
	if err != nil {
		return err
	}
	defer control.Close()
	raw, err := ipc.ReadOwnerBootstrapPipe(ctx, *bootstrapFD)
	if err != nil {
		return err
	}
	bootstrap, err := contract.DecodeOwnerTransportBootstrap(raw)
	if err != nil {
		return err
	}
	return host.RunOwnerTransport(ctx, bootstrap, control)
}
