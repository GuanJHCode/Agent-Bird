package host

import (
	"errors"
	"path/filepath"
	"reflect"
)

func verifyCodexConfigLayers(result map[string]any, runtime string) error {
	invalid := errors.New("codex_config_layers_unverified")
	layers, ok := result["layers"].([]any)
	if !ok || len(layers) == 0 || len(layers) > 64 {
		return invalid
	}
	users, flags := 0, 0
	for _, raw := range layers {
		layer, ok := raw.(map[string]any)
		if !ok {
			return invalid
		}
		name, ok := layer["name"].(map[string]any)
		if !ok {
			return invalid
		}
		config, ok := layer["config"].(map[string]any)
		if !ok {
			return invalid
		}
		if layer["disabledReason"] != nil {
			return invalid
		}
		switch name["type"] {
		case "user":
			users++
			if name["profile"] != nil {
				return invalid
			}
		case "sessionFlags":
			flags++
			if err := verifyCodexSessionFlags(config, runtime); err != nil {
				return err
			}
		case "system", "project":
			if len(config) != 0 {
				return invalid
			}
		default:
			return invalid
		}
	}
	if users != 1 || flags != 1 {
		return invalid
	}
	return verifyCodexEffectiveRestrictions(result, runtime)
}

func codexExpectedRuntimeConfig(runtime string) map[string]any {
	features := map[string]any{}
	for _, name := range codexDisabledFeatures() {
		features[name] = false
	}
	return map[string]any{
		"features":   features,
		"web_search": "disabled",
		"tools":      map[string]any{"experimental_request_user_input": map[string]any{"enabled": false}},
		"agents":     map[string]any{"enabled": false}, "notify": []any{},
		"sqlite_home": filepath.Join(runtime, "sqlite"), "log_dir": filepath.Join(runtime, "log"),
	}
}
func verifyCodexSessionFlags(config map[string]any, runtime string) error {
	if !filepath.IsAbs(runtime) || !reflect.DeepEqual(config, codexExpectedRuntimeConfig(runtime)) {
		return errors.New("codex_session_flags_unverified")
	}
	return nil
}
func verifyCodexEffectiveRestrictions(result map[string]any, runtime string) error {
	invalid := errors.New("codex_effective_policy_unverified")
	config, ok := result["config"].(map[string]any)
	if !ok {
		return invalid
	}
	for key, want := range codexExpectedRuntimeConfig(runtime) {
		// Fixed config/read exposes only legacy tools.web_search. The newer
		// user-input switch is proven by the exact, highest-priority session
		// flags above; it is not represented by this effective API projection.
		if key == "tools" {
			continue
		}
		if nested, ok := want.(map[string]any); ok {
			actual, ok := config[key].(map[string]any)
			if !ok {
				return invalid
			}
			for key, want := range nested {
				if !reflect.DeepEqual(actual[key], want) {
					return invalid
				}
			}
		} else if !reflect.DeepEqual(config[key], want) {
			return invalid
		}
	}
	return nil
}
