package main

import (
	"context"
	"encoding/json"
	"flag"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/hookcli"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
	"io"
	"os"
)

func bridgeCapabilities(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("bridge capabilities", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	provider := fs.String("provider", "", "provider family")
	binary := fs.String("binary", "", "explicit executable path")
	probe := fs.Bool("probe-version", false, "bounded read-only --version observation")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *provider == "" {
		return codeError("invalid_args")
	}
	report, err := hookcli.DiscoverCapabilities(ctx, *provider, hookcli.CapabilityOptions{Binary: *binary, ProbeVersion: *probe})
	if err != nil {
		return bridgeCleanError(err)
	}
	executable, e := os.Executable()
	if e != nil {
		return codeError("package_executable_invalid")
	}
	kind, installed, e := install.InstalledExecutableKind(executable)
	if e != nil {
		return bridgeCleanError(e)
	}
	if installed {
		report = packageCapabilityLimit(report, kind)
	}
	return bridgeCleanError(json.NewEncoder(stdout).Encode(report))
}
