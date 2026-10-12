package codexsession

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexconfig"
	"testing"
)

func TestPluginCompatibilityModelKeepsReadOnlyProjection(t *testing.T) {
	projection, err := codexconfig.Project([]byte("model = \"gpt-6.1-sol\"\nmodel_reasoning_effort = \"high\"\nservice_tier = \"priority\"\n"), codexconfig.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	result := runScenario(t, fixtureScenario{projection: projection, checkRequest: func(method string, raw json.RawMessage, resume bool) error {
		if method == "thread/start" || method == "turn/start" {
			var p map[string]any
			if json.Unmarshal(raw, &p) != nil || p["model"] != "gpt-5.6-sol" {
				return fmt.Errorf("explicit compatibility model missing")
			}
			p["model"] = "gpt-5.4"
			raw, _ = json.Marshal(p)
		}
		return hostToolChecker(method, raw, resume)
	}, input: func(o *Options) {
		o.ModelOverride = "gpt-5.6-sol"
		o.HostTools = &HostTools{OnTool: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil }}
	}, mutate: func(method string, value map[string]any) {
		if method == "thread/start" {
			value["model"] = "gpt-5.6-sol"
		}
		if method == "config/read" {
			obj(value["config"])["model"] = "gpt-6.1-sol"
		}
	}})
	if result.err != nil || result.out.Binding.Model != "gpt-5.6-sol" {
		t.Fatal(result.err, result.out)
	}
	for _, kind := range []string{"legacy", "resume", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			r := runScenario(t, fixtureScenario{input: func(o *Options) {
				o.ModelOverride = "gpt-5.6-sol"
				if kind != "legacy" {
					o.HostTools = &HostTools{OnTool: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil }}
				}
				if kind == "resume" {
					o.ResumeThreadID = "thread-1"
				}
				if kind == "unknown" {
					o.ModelOverride = "arbitrary"
				}
			}})
			if r.err != ErrInput {
				t.Fatal(r.err)
			}
		})
	}
}

// Pinned 0.154.0 config/read retains "fast", but thread/start returns its
// canonical wire spelling "priority". These describe the same service tier.
func TestPluginFastTierUsesPinnedCanonicalPriority(t *testing.T) {
	projection, err := codexconfig.Project([]byte("service_tier = 'fast'\n"), codexconfig.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	result := runScenario(t, fixtureScenario{projection: projection, mutate: func(method string, value map[string]any) {
		if method == "config/read" {
			obj(value["config"])["service_tier"] = "fast"
		}
	}, checkRequest: func(method string, raw json.RawMessage, resume bool) error {
		if method == "thread/start" || method == "turn/start" {
			var params map[string]any
			if json.Unmarshal(raw, &params) != nil || params["serviceTier"] != "priority" {
				return fmt.Errorf("tier alias not canonicalized")
			}
		}
		return nil
	}})
	if result.err != nil {
		t.Fatal(result.err)
	}
}
