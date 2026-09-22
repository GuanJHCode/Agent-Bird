package host

import "testing"

func TestCodexLayersRejectUnverifiedManagedPolicyAndProfiles(t *testing.T) {
	layer := func(kind string, config map[string]any) map[string]any {
		return map[string]any{"name": map[string]any{"type": kind}, "config": config}
	}
	projection := map[string]any{
		"features": map[string]any{"multi_agent": false, "multi_agent_v2": false, "hooks": false, "plugins": false, "apps": false, "shell_snapshot": false, "memories": false},
		"agents":   map[string]any{"enabled": false}, "notify": []any{}, "sqlite_home": "/runtime/sqlite", "log_dir": "/runtime/log",
	}
	valid := map[string]any{"config": projection, "layers": []any{layer("sessionFlags", projection), layer("user", map[string]any{"model": "fixture"}), layer("system", map[string]any{})}}
	if err := verifyCodexConfigLayers(valid, "/runtime"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"mdm", "cloud", "legacyManagedConfigTomlFromFile", "unknown"} {
		if err := verifyCodexConfigLayers(map[string]any{"layers": []any{layer(kind, map[string]any{})}}, "/runtime"); err == nil {
			t.Fatal("unverified config authority accepted")
		}
	}
	if err := verifyCodexConfigLayers(map[string]any{}, "/runtime"); err == nil {
		t.Fatal("missing provenance accepted")
	}
	profile := layer("user", map[string]any{})
	profile["name"].(map[string]any)["profile"] = "custom"
	if err := verifyCodexConfigLayers(map[string]any{"layers": []any{profile}}, "/runtime"); err == nil {
		t.Fatal("unmapped profile accepted")
	}
	if err := verifyCodexConfigLayers(map[string]any{"layers": []any{layer("project", map[string]any{"model_provider": "custom"})}}, "/runtime"); err == nil {
		t.Fatal("unverified external layer accepted")
	}
}

func TestCodexLayersRejectUnexpectedSessionOverrides(t *testing.T) {
	for _, config := range []map[string]any{
		{"approval_policy": "never"},
		{"features": map[string]any{"hooks": true}},
		{"notify": []any{"unexpected-callback"}},
	} {
		result := map[string]any{"layers": []any{
			map[string]any{"name": map[string]any{"type": "user"}, "config": map[string]any{}},
			map[string]any{"name": map[string]any{"type": "sessionFlags"}, "config": config},
		}}
		if err := verifyCodexConfigLayers(result, "/runtime"); err == nil {
			t.Fatal("unexpected session override accepted")
		}
	}
}

func TestCodexLayersRequireCompleteRuntimeProjection(t *testing.T) {
	result := map[string]any{"layers": []any{
		map[string]any{"name": map[string]any{"type": "user"}, "config": map[string]any{}},
		map[string]any{"name": map[string]any{"type": "sessionFlags"}, "config": map[string]any{}},
	}}
	if err := verifyCodexConfigLayers(result, "/runtime"); err == nil {
		t.Fatal("missing worker restrictions accepted")
	}
}
