package main

import (
	"context"
	"os"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitfilter"
)

// Dedicated internal helper: routing contains comparison values only. No
// Coordinator, capability file, launch token or caller control token is read.
func filterCarrierHelper(ctx context.Context, args []string) error {
	if len(args) != 2 || args[0] != "--routing" {
		return codeError("invalid_args")
	}
	route, err := gitfilter.DecodeCarrierHelperRoute(args[1])
	if err != nil {
		return err
	}
	return gitfilter.RunCarrierHelper(ctx, route, os.Stdin, os.Stdout)
}
