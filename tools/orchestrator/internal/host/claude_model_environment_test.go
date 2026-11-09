package host

import (
	"context"
	"strings"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
)

func gatewayChildEnvironment(entries []string) map[string]string {
	values := map[string]string{}
	for _, entry := range entries {
		name, value, _ := strings.Cut(entry, "=")
		values[name] = value
	}
	return values
}

func TestClaudeGatewayCLIDefaultPreservesSourceModelSelection(t *testing.T) {
	cmd, profile, scratch, source := claudeGatewayFixture(t)
	profile.CLIModelDefault = true
	want := map[string]string{
		"ANTHROPIC_MODEL":                "source-primary[1M]",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "source-small",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "source-balanced",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "source-large",
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  "source-fable",
		"ANTHROPIC_SMALL_FAST_MODEL":     "source-fast",
	}
	for name, value := range want {
		source.env[name] = value
	}
	source.env["ANTHROPIC_CUSTOM_HEADERS"] = "Authorization: unrelated-secret"
	source.env["ANTHROPIC_BETAS"] = "unrequested-beta"
	source.env["ANTHROPIC_DEFAULT_SONNET_MODEL_NAME"] = "display-label-only"
	prepared, err := prepareClaudeGateway(context.Background(), cmd, profile, scratch, source)
	if err != nil {
		t.Fatal(err)
	}
	got := gatewayChildEnvironment(prepared.Env)
	for name, value := range want {
		if actual, present := got[name]; !present || actual != value {
			t.Errorf("source default model selection lost or changed: %s", name)
		}
	}
	for _, name := range []string{"ANTHROPIC_CUSTOM_HEADERS", "ANTHROPIC_BETAS", "ANTHROPIC_DEFAULT_SONNET_MODEL_NAME", "NODE_OPTIONS", "HTTPS_PROXY", "DYLD_INSERT_LIBRARIES"} {
		if _, present := got[name]; present {
			t.Errorf("unrelated source environment crossed gateway boundary: %s", name)
		}
	}
	if !prepared.ExactEnv || profile.Model != "" || !profile.CLIModelDefault {
		t.Fatal("source defaults changed profile or environment isolation")
	}
	if got["ANTHROPIC_AUTH_TOKEN"] != source.env["ANTHROPIC_AUTH_TOKEN"] || got["ANTHROPIC_BASE_URL"] != source.env["ANTHROPIC_BASE_URL"] {
		t.Fatal("model preservation changed existing route/auth")
	}
}

func TestClaudeGatewaySourceModelDefaultsRetainAbsenceAndEmptyValues(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "explicit-empty"}[present], func(t *testing.T) {
			cmd, profile, scratch, source := claudeGatewayFixture(t)
			profile.CLIModelDefault = true
			if present {
				source.env["ANTHROPIC_MODEL"] = ""
			}
			prepared, err := prepareClaudeGateway(context.Background(), cmd, profile, scratch, source)
			if err != nil {
				t.Fatal(err)
			}
			got := gatewayChildEnvironment(prepared.Env)
			if value, found := got["ANTHROPIC_MODEL"]; found != present || value != "" {
				t.Fatal("source empty/absent model selection changed")
			}
			for _, name := range []string{"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_SMALL_FAST_MODEL"} {
				if _, found := got[name]; found {
					t.Fatalf("missing source selector invented: %s", name)
				}
			}
		})
	}
}

func TestClaudeGatewayExplicitModelKeepsOriginalOverrideContract(t *testing.T) {
	cmd, profile, scratch, source := claudeGatewayFixture(t)
	profile.Model = adapter.ModelID("explicit-model")
	for _, name := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_SMALL_FAST_MODEL"} {
		source.env[name] = "different-source-model"
	}
	prepared, err := prepareClaudeGateway(context.Background(), cmd, profile, scratch, source)
	if err != nil {
		t.Fatal(err)
	}
	got := gatewayChildEnvironment(prepared.Env)
	for _, name := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL"} {
		if got[name] != "explicit-model" {
			t.Fatalf("explicit override changed: %s", name)
		}
	}
	for _, name := range []string{"ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_SMALL_FAST_MODEL"} {
		if _, present := got[name]; present {
			t.Fatalf("explicit override unexpectedly inherited source selector: %s", name)
		}
	}
}
