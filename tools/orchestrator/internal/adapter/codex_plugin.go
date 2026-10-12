package adapter

import "errors"

// The version distinguishes fresh plugin-tool threads from the sealed M3
// protocol. Old sessions cannot gain tools by loading a newer Agent Bird build.
const CodexPluginBackend = "codex-app-server-plugin-read-v1"

func BuildCodexPluginInvocation(req Request) (Invocation, error) {
	if req.Provider != ProviderCodex || req.Profile == nil || req.Profile.CodexHostToolsVersion != 1 || req.Continuation != nil || req.Session.Kind != "" || req.Session.ID != "" {
		return Invocation{}, errors.New("codex_plugin_profile_invalid")
	}
	original := *req.Profile
	legacy := original
	legacy.CodexHostToolsVersion = 0
	req.Profile = &legacy
	inv, err := BuildManagedCodexInvocation(req)
	if err != nil {
		return Invocation{}, err
	}
	inv.profile = &original
	inv.managedCodex.Backend = CodexPluginBackend
	return inv, nil
}
