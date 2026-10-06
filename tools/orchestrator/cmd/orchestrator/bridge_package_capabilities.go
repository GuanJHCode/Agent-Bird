package main

import "github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/hookcli"

func packageCapabilityLimit(report hookcli.CapabilityReport, kind string) hookcli.CapabilityReport {
	if kind == "legacy" && (report.Provider == "claude" || report.Provider == "agy") {
		report.Mode = "explicit_collect"
		report.HookRegistered = false
		report.AutomaticStatus = "PACKAGE_HOOK_UNAVAILABLE"
		report.Limitations = append(report.Limitations, "installed_legacy_package_has_no_registered_hook_assets")
	}
	return report
}
