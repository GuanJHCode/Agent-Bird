package codexsession

import (
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexconfig"
	"reflect"
	"testing"
)

// Actual M1/M3 on net.Pipe; all peer and transport jobs are joined by runScenario.
func TestRunBoundRuntimePathsRejectsBeforeThreadAndTurn(t *testing.T) {
	p, err := codexconfig.Project([]byte("model=\"gpt-5.4\"\nmodel_reasoning_effort=\"high\"\nservice_tier=\"priority\"\n"), codexconfig.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	p, err = p.BindRuntimePaths("/managed/sqlite", "/managed/log")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"sqlite_home", "log_dir"} {
		for _, kind := range []string{"matching", "missing", "null", "wrong_type", "different"} {
			t.Run(key+"/"+kind, func(t *testing.T) {
				result := runScenario(t, fixtureScenario{projection: p, mutate: func(method string, response map[string]any) {
					if method != "config/read" {
						return
					}
					c := response["config"].(map[string]any)
					c["sqlite_home"] = "/managed/sqlite"
					c["log_dir"] = "/managed/log"
					switch kind {
					case "missing":
						delete(c, key)
					case "null":
						c[key] = nil
					case "wrong_type":
						c[key] = false
					case "different":
						c[key] = "/other/canonical/path"
					}
				}})
				if kind == "matching" {
					if result.err != nil || result.callbacks != 1 || result.out.Completion.Kind != "technical_question" {
						t.Fatal("matching managed paths rejected", result.err)
					}
				} else if !errors.Is(result.err, ErrPolicy) || result.err.Error() != ErrPolicy.Error() || result.callbacks != 0 || result.out.Completion.Kind != "" || !reflect.DeepEqual(result.methods, []string{"initialize", "initialized", "config/read"}) {
					t.Fatal("wrong runtime path reached binding/thread/turn", result.err, result.callbacks, result.methods)
				}
			})
		}
	}
}
