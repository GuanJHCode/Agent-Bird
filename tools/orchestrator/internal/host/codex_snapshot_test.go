package host

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestCodexSnapshotRebasesPathsAndDropsDisabledSecretCarriers(t *testing.T) {
	raw := []byte(`model="fixture"
approval_policy="on-request"
sandbox_mode="workspace-write"
model_instructions_file="../instructions.md"
js_repl_node_module_dirs=["modules", "~/modules"]
notify=["private-callback"]
[mcp_servers.fixture]
command="private-command"
[mcp_servers.fixture.env]
TOKEN="fixture-secret"
[agents.reviewer]
config_file="roles/review.toml"
[skills]
config=[{path="skills/review",enabled=false}]
`)
	snapshot, err := compileCodexSnapshot(raw, "/source/codex", "/source/user")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(snapshot), "fixture-secret") || strings.Contains(string(snapshot), "private-command") || strings.Contains(string(snapshot), "private-callback") {
		t.Fatal("disabled credentials/callback copied")
	}
	var cfg map[string]any
	if err := toml.Unmarshal(snapshot, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["approval_policy"] != "on-request" || cfg["sandbox_mode"] != "workspace-write" || cfg["model"] != "fixture" {
		t.Fatal("source policy/model changed")
	}
	if cfg["model_instructions_file"] != "/source/instructions.md" {
		t.Fatal("relative instruction path changed semantics")
	}
	agents := cfg["agents"].(map[string]any)
	if agents["reviewer"].(map[string]any)["config_file"] != filepath.Join("/source/codex", "roles/review.toml") {
		t.Fatal("role path not rebased")
	}
	dirs := cfg["js_repl_node_module_dirs"].([]any)
	if dirs[0] != "/source/codex/modules" || dirs[1] != "/source/user/modules" {
		t.Fatal("module paths not rebased")
	}
}

func TestCodexSnapshotRejectsUnknownOrSecretBearingInput(t *testing.T) {
	for _, raw := range []string{
		`unknown_path="../../../../etc"`,
		"[shell_environment_policy.set]\nAPI_TOKEN=\"fixture-secret\"",
		"[model_providers.custom]\nexperimental_bearer_token=\"fixture-secret\"",
		"[otel]\nexporter=\"otlp-http\"",
		"[permissions]\nprofile=\"custom\"",
		`profile="custom"`,
	} {
		if _, err := compileCodexSnapshot([]byte(raw), "/source/codex", "/source/user"); err == nil {
			t.Fatal("unverified input accepted")
		}
	}
}

func TestCodexSnapshotPreservesAllSupportedPathBases(t *testing.T) {
	raw := []byte(`model_instructions_file="instructions.md"
js_repl_node_path="bin/node"
model_catalog_json="catalog.json"
experimental_compact_prompt_file="compact.md"
sqlite_home="db"
log_dir="logs"
[sandbox_workspace_write]
writable_roots=["../work", "~/work"]
[profiles.fixture]
model_instructions_file="profile.md"
model_catalog_json="profile.json"
js_repl_node_path="bin/profile-node"
js_repl_node_module_dirs=["modules"]
experimental_compact_prompt_file="profile-compact.md"
`)
	snapshot, err := compileCodexSnapshot(raw, "/source/codex", "/source/user")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := toml.Unmarshal(snapshot, &cfg); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"model_instructions_file": "/source/codex/instructions.md", "js_repl_node_path": "/source/codex/bin/node",
		"model_catalog_json": "/source/codex/catalog.json", "experimental_compact_prompt_file": "/source/codex/compact.md",
		"sqlite_home": "/source/codex/db", "log_dir": "/source/codex/logs",
	} {
		if cfg[key] != want {
			t.Errorf("%s did not retain its source base", key)
		}
	}
	roots := cfg["sandbox_workspace_write"].(map[string]any)["writable_roots"].([]any)
	if len(roots) != 2 || roots[0] != "/source/work" || roots[1] != "/source/user/work" {
		t.Fatal("workspace roots changed meaning")
	}
	profile := cfg["profiles"].(map[string]any)["fixture"].(map[string]any)
	for key, want := range map[string]string{
		"model_instructions_file": "/source/codex/profile.md", "model_catalog_json": "/source/codex/profile.json",
		"js_repl_node_path": "/source/codex/bin/profile-node", "experimental_compact_prompt_file": "/source/codex/profile-compact.md",
	} {
		if profile[key] != want {
			t.Errorf("profile %s did not retain its source base", key)
		}
	}
	modules := profile["js_repl_node_module_dirs"].([]any)
	if len(modules) != 1 || modules[0] != "/source/codex/modules" {
		t.Fatal("profile module base changed")
	}
}
