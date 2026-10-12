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
