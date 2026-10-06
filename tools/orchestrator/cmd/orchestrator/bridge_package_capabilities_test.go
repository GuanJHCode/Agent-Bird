package main

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/hookcli"
	"testing"
)

func TestB07LegacyPackageCapabilityCannotClaimHookAssets(t *testing.T) {
	report := hookcli.CapabilityReport{Provider: "claude", Mode: "hook_pull", HookRegistered: true, AutomaticStatus: "REGISTERED_UNVERIFIED", NativeAcceptance: "WAIT_AUTH"}
	report = packageCapabilityLimit(report, "legacy")
	if report.Mode != "explicit_collect" || report.HookRegistered || report.AutomaticStatus != "PACKAGE_HOOK_UNAVAILABLE" || report.NativeAcceptance != "WAIT_AUTH" {
		t.Fatalf("legacy claims Hook assets: %+v", report)
	}
	newReport := hookcli.CapabilityReport{Provider: "agy", Mode: "hook_pull", HookRegistered: true, AutomaticStatus: "REGISTERED_UNVERIFIED", NativeAcceptance: "WAIT_AUTH"}
	if got := packageCapabilityLimit(newReport, "collect-only-hooks"); got.Mode != "hook_pull" || !got.HookRegistered || got.NativeAcceptance != "WAIT_AUTH" {
		t.Fatal("new capability changed")
	}
}
