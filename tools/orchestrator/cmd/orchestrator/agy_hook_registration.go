package main

import "github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/hookcli"

func init() {
	if err := hookcli.RegisterProvider("agy", hookcli.NormalizeAGY); err != nil {
		panic("agy_hook_registration_failed")
	}
}
