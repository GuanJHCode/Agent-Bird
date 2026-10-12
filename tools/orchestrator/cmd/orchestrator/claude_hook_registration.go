package main

import "github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/hookcli"

// Registration belongs to the CLI executable's startup. Importing hookcli in
// another application never implicitly installs a provider or native capability.
func init() {
	if err := hookcli.RegisterProvider("claude", hookcli.NormalizeClaude); err != nil {
		panic("claude_hook_registration_failed")
	}
}
