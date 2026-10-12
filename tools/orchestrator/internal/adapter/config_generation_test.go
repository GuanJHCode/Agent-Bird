package adapter

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfigGenerationPayloadClosedPair(t *testing.T) {
	ref := `{"schema_version":1,"owner_scope_sha256":"` + strings.Repeat("a", 64) + `","generation_id":"` + strings.Repeat("b", 64) + `","manifest_sha256":"` + strings.Repeat("c", 64) + `","package_version":"1.0.0"}`
	valid := `{"kind":"fake","config_generation":` + ref + `,"config_model_source":"not-applicable"}`
	for _, tc := range []struct {
		name  string
		raw   string
		valid bool
	}{
		{"legacy", `{"kind":"fake"}`, true},
		{"generation", valid, true},
		{"saved-default", strings.Replace(valid, `"config_model_source":"not-applicable"`, `"profile":{"model":""},"provider":"antigravity-cli","config_model_source":"saved-default"`, 1), true},
		{"explicit", strings.Replace(valid, `"config_model_source":"not-applicable"`, `"profile":{"model":"explicit-model"},"provider":"claude-code","config_model_source":"explicit"`, 1), true},
		{"cli-default", strings.Replace(valid, `"config_model_source":"not-applicable"`, `"profile":{"cli_model_default":true},"provider":"claude-code","config_model_source":"cli-default"`, 1), true},
		{"missing-source", `{"kind":"fake","config_generation":` + ref + `}`, false},
		{"missing-ref", `{"kind":"fake","config_model_source":"not-applicable"}`, false},
		{"null-ref", strings.Replace(valid, ref, `null`, 1), false},
		{"null-source", strings.Replace(valid, `"not-applicable"`, `null`, 1), false},
		{"source-alias", strings.Replace(valid, `"config_model_source"`, `"Config_Model_Source"`, 1), false},
		{"ref-alias", strings.Replace(valid, `"config_generation"`, `"CONFIG_GENERATION"`, 1), false},
		{"ref-field-alias", strings.Replace(valid, `"schema_version"`, `"Schema_Version"`, 1), false},
		{"duplicate-ref-field", strings.Replace(valid, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1), false},
		{"duplicate-source", strings.Replace(valid, `"config_model_source":"not-applicable"`, `"config_model_source":"not-applicable","config_model_source":"not-applicable"`, 1), false},
		{"unknown-ref-field", strings.Replace(valid, `"schema_version":1`, `"schema_version":1,"executable":"/untrusted"`, 1), false},
		{"partial-ref", strings.Replace(valid, `"schema_version":1,`, ``, 1), false},
		{"bad-hash", strings.Replace(valid, strings.Repeat("a", 64), strings.Repeat("A", 64), 1), false},
		{"bad-package", strings.Replace(valid, `1.0.0`, `../other`, 1), false},
		{"unknown-source", strings.Replace(valid, `not-applicable`, `live-default`, 1), false},
		{"profile-not-applicable", strings.Replace(valid, `"kind":"fake"`, `"kind":"fake","profile":{"model":"x"}`, 1), false},
		{"no-profile-default", strings.Replace(valid, `not-applicable`, `saved-default`, 1), false},
		{"empty-explicit", strings.Replace(valid, `"config_model_source":"not-applicable"`, `"profile":{"model":""},"config_model_source":"explicit"`, 1), false},
		{"cli-explicit-conflict", strings.Replace(valid, `"config_model_source":"not-applicable"`, `"profile":{"cli_model_default":true,"model":"x"},"config_model_source":"cli-default"`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := DecodeInvocationPayload([]byte(tc.raw))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, decoder error=%v", tc.valid, err)
			}
			if tc.valid && tc.name != "legacy" {
				encoded, err := json.Marshal(p)
				if err != nil || !strings.Contains(string(encoded), `"config_generation"`) || !strings.Contains(string(encoded), `"config_model_source"`) {
					t.Fatalf("lost immutable generation/provenance: %s, %v", encoded, err)
				}
			}
		})
	}
}
