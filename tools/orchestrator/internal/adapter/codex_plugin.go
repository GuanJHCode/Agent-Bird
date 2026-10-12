package adapter

import "errors"

// Fresh plugin threads have a distinct backend from the sealed M3 protocol.
const CodexPluginBackend = "codex-app-server-plugin-read-v1"
const CodexPluginWriteBackend = "codex-app-server-plugin-write-v1"
const CodexPluginCompatibilityModel ModelID = "gpt-5.6-sol"

func BuildCodexPluginInvocation(req Request) (Invocation, error) {
	if req.Provider != ProviderCodex || req.Profile == nil || req.Profile.CodexHostToolsVersion != 1 || req.Continuation != nil || req.Session.Kind != "" || req.Session.ID != "" {
		return Invocation{}, errors.New("codex_plugin_profile_invalid")
	}
	original := *req.Profile
	workspace := cloneWorkspace(req.Workspace)
	write := original.Role == Implementer && original.Permission == WorkspaceWrite
	if write {
		if validateWorkspace(req) != nil || workspace == nil || !workspace.AutoDirectory || workspace.Rework != nil || workspace.NewBaseRework != nil || !managedCodexPath(workspace.RepoRoot) || len(workspace.Paths) == 0 || len(workspace.Paths) > 256 {
			return Invocation{}, errors.New("codex_plugin_candidate_invalid")
		}
	} else if workspace != nil {
		return Invocation{}, errors.New("codex_plugin_candidate_invalid")
	}
	legacy := original
	if !original.CLIModelDefault {
		if original.Model != CodexPluginCompatibilityModel {
			return Invocation{}, errors.New("codex_plugin_model_unsupported")
		}
		legacy.CLIModelDefault, legacy.Model = true, ""
	}
	legacy.CodexHostToolsVersion = 0
	if write {
		legacy.Role, legacy.Permission = Reviewer, ReadOnly
	}
	req.Profile, req.Workspace = &legacy, nil
	inv, err := BuildManagedCodexInvocation(req)
	if err != nil {
		return Invocation{}, err
	}
	inv.profile, inv.workspace = &original, workspace
	inv.managedCodex.Backend = CodexPluginBackend
	if write {
		inv.managedCodex.Backend = CodexPluginWriteBackend
	}
	return inv, nil
}
