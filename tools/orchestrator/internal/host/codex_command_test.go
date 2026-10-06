package host

import (
	"context"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// Pure construction: no sandbox-exec/version/provider/helper is launched.
func TestCodexManagedCommandRestrictsStateWithoutChangingSourceDefaults(t *testing.T) {
	h, g, inv, env := codexOriginFixture(t)
	p, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
	if err != nil {
		t.Fatal(err)
	}
	env = append(env, "PATH=/synthetic/bin", "C06_ORDINARY_ENV=SYNTHETIC_ORIGINAL_ENV_NOT_EXPORTED", "ORCHESTRATOR_REPORT_EXECUTABLE=/forbidden/helper", "ORCHESTRATOR_REPORT_CAPABILITY=/forbidden/cap")
	cmd, launched, err := codexManagedCommand(context.Background(), p, env, h.spoolRoot)
	if err != nil {
		t.Fatal(err)
	}
	if launched.Launch.Path != cmd.Path || !codexHexDigest(launched.Launch.SHA256) || p.Launch.Path != "" || p.Launch.SHA256 != "" {
		t.Fatal("trusted actual wrapper identity not independently bound")
	}
	if cmd.Path != "/usr/bin/sandbox-exec" || cmd.PinnedPath != p.Origin.Lock.Binary.Path || cmd.PinnedSHA256 != p.Origin.Lock.Binary.SHA256 || cmd.Dir != p.Origin.Workspace || !cmd.ExactEnv || cmd.Stdin != nil || cmd.Stdout != nil || len(cmd.Args) != 81 {
		t.Fatal("managed original-launch surface drift")
	}
	if cmd.Args[0] != "-p" || cmd.Args[2] != cmd.PinnedPath || cmd.Args[3] != "app-server" || cmd.Args[4] != "--strict-config" {
		t.Fatal("legacy/extra provider command substituted")
	}
	values := map[string]string{}
	for _, entry := range cmd.Env {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	if values["HOME"] != strings.TrimPrefix(env[0], "HOME=") || values["CODEX_HOME"] != p.Layout.Home.Path || values["TMPDIR"] != p.Layout.Auxiliary.Runtime[4].Path || values["PATH"] != "/synthetic/bin" || values["C06_ORDINARY_ENV"] != "SYNTHETIC_ORIGINAL_ENV_NOT_EXPORTED" || values["ORCHESTRATOR_REPORT_EXECUTABLE"] != "" || values["ORCHESTRATOR_REPORT_CAPABILITY"] != "" {
		t.Fatal("source account/environment changed or helper authority retained")
	}
	var overrides map[string]any
	for i := 5; i < len(cmd.Args); i += 2 {
		if cmd.Args[i] != "-c" {
			t.Fatal("unexpected native argument")
		}
		var value map[string]any
		if toml.Unmarshal([]byte(cmd.Args[i+1]), &value) != nil {
			t.Fatal("override not valid fixed TOML")
		}
		if overrides == nil {
			overrides = map[string]any{}
		}
		for key, v := range value {
			if key == "sqlite_home" || key == "log_dir" {
				overrides[key] = v
			}
		}
	}
	if overrides["sqlite_home"] != p.Layout.SQLite.Path || overrides["log_dir"] != p.Layout.Auxiliary.Runtime[3].Path {
		t.Fatal("anchored exact runtime path override missing")
	}
	rules := cmd.Args[1]
	if strings.Contains(rules, "(allow file-write* (subpath "+quotedPath(p.Layout.Home.Path)+"))") || strings.Contains(rules, "models_cache.json") || strings.Contains(rules, "session_index.jsonl") || strings.Contains(rules, "/forbidden/") {
		t.Fatal("whole-home/cache/helper authority expanded")
	}
	if strings.Count(rules, "(allow file-write* (subpath ") != 7 || !strings.Contains(rules, "(deny file-read* (subpath "+quotedPath(h.spoolRoot)+"))") || strings.Contains(rules, "SYNTHETIC_ORIGINAL_ENV") {
		t.Fatal("fixed write/read scope or diagnostics changed")
	}
}

func quotedPath(path string) string { return "\"" + path + "\"" } // Fixture paths contain no escapes.
