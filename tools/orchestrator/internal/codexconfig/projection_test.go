package codexconfig

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

const currentSource = `model = "fixture-model-1"
model_provider = "openai"
model_reasoning_effort = "ultra-fixture"
service_tier = "fixture-tier"
review_model = "fixture-review-1"
model_context_window = 9007199254740993
model_auto_compact_token_limit = 720000
model_auto_compact_token_limit_scope = "body_after_prefix"
plan_mode_reasoning_effort = "high"
[agents]
job_max_runtime_seconds = "SECRET_SENTINEL_DO_NOT_COPY"
[tui]
screen_reader_detection_done = "SECRET_SENTINEL_DO_NOT_COPY"
[mcp_servers.fixture]
command = "SECRET_SENTINEL_DO_NOT_COPY"
[mcp_servers.fixture.http_headers]
Authorization = "SECRET_SENTINEL_DO_NOT_COPY"
[hooks]
command = "SECRET_SENTINEL_DO_NOT_COPY"
[features]
hooks = true
plugins = true
`

func TestProjectPreservesSettingsAndDropsSourceSecrets(t *testing.T) {
	for _, mode := range []Mode{ReadOnly, WorkspaceWrite} {
		t.Run(string(mode), func(t *testing.T) {
			p, err := Project([]byte(currentSource), mode)
			if err != nil {
				t.Fatalf("supported source must project: %v", err)
			}
			config := p.ConfigTOML()
			var generated map[string]any
			if err := toml.Unmarshal(config, &generated); err != nil {
				t.Fatalf("generated config is not valid TOML: %v", err)
			}
			want := map[string]any{
				"model": "fixture-model-1", "model_provider": "openai", "model_reasoning_effort": "ultra-fixture",
				"service_tier": "fixture-tier", "review_model": "fixture-review-1", "model_context_window": int64(9007199254740993),
				"model_auto_compact_token_limit": int64(720000), "model_auto_compact_token_limit_scope": "body_after_prefix",
				"plan_mode_reasoning_effort": "high", "cli_auth_credentials_store": "file", "sandbox_mode": string(mode),
				"approval_policy": "on-request", "approvals_reviewer": "user",
			}
			if !reflect.DeepEqual(generated, want) {
				t.Fatal("generated TOML differs from the nine exact typed settings and four managed controls")
			}
			for _, value := range []string{"fixture-model-1", "openai", "ultra-fixture", "fixture-tier", "fixture-review-1", "9007199254740993", "720000", "body_after_prefix", "high", string(mode), "file", "on-request", "user"} {
				if !bytes.Contains(config, []byte(value)) {
					t.Errorf("missing preserved or managed fixture value %q", value)
				}
			}
			if bytes.Contains(config, []byte("SECRET_SENTINEL")) || strings.Contains(strings.Join(p.ManagedOverrides(), "\n"), "SECRET_SENTINEL") {
				t.Fatal("source secret leaked into managed output")
			}
			if len(p.ManagedOverrides()) != 36 {
				t.Errorf("want 36 managed controls, got %d", len(p.ManagedOverrides()))
			}
		})
	}
}

func mustProject(t *testing.T, source string, mode Mode) *Projection {
	t.Helper()
	p, err := Project([]byte(source), mode)
	if err != nil {
		t.Fatalf("supported synthetic projection: %v", err)
	}
	return p
}

func TestProjectTOMLSemantics(t *testing.T) {
	sources := map[string]string{
		"quoted_keys": `"model" = 'fixture:model/1'
"model_auto_compact_token_limit_scope" = 'total'
"agents"."job_max_runtime_seconds" = 'SECRET_SENTINEL'
"tui"."screen_reader_detection_done" = true # new GUI field
`,
		"inline_ignored_tables": `model = 'fixture:model/1'
model_auto_compact_token_limit_scope = 'total'
agents = { nested = { unknown = ['SECRET_SENTINEL', 1, true] } }
tui = { unknown = { value = 'SECRET_SENTINEL' } }
`,
		"multiline_ignored_strings": "model = 'fixture:model/1'\nmodel_auto_compact_token_limit_scope = 'total'\nhooks = '''SECRET_SENTINEL\nsecond line'''\n",
	}
	var first []byte
	for name, source := range sources {
		t.Run(name, func(t *testing.T) {
			p := mustProject(t, source, ReadOnly)
			if bytes.Contains(p.ConfigTOML(), []byte("SECRET_SENTINEL")) {
				t.Fatal("ignored source data survived")
			}
			if first == nil {
				first = p.ConfigTOML()
			} else if !bytes.Equal(first, p.ConfigTOML()) {
				t.Fatal("equivalent TOML syntax changed deterministic output")
			}
		})
	}
}

func TestProjectOptionalSettingsRemainAbsent(t *testing.T) {
	for name, source := range map[string]string{"empty": "", "explicit_file_auth": `cli_auth_credentials_store = "file"`, "empty_definitions": "profiles = {}\nmodel_providers = {}"} {
		t.Run(name, func(t *testing.T) {
			p := mustProject(t, source, ReadOnly)
			var config map[string]any
			if err := toml.Unmarshal(p.ConfigTOML(), &config); err != nil {
				t.Fatal(err)
			}
			if len(config) != 4 {
				t.Fatal("optional model settings were added or source context retained")
			}
		})
	}
}

func TestProjectDiscardsEveryManagedSourceArea(t *testing.T) {
	for _, key := range []string{"agents", "approval_policy", "approvals_reviewer", "apps", "check_for_update_on_startup", "desktop", "features", "history", "hooks", "marketplaces", "mcp_servers", "memories", "notice", "notify", "plugins", "projects", "sandbox_mode", "shell_environment_policy", "skills", "tui"} {
		t.Run(key, func(t *testing.T) {
			p := mustProject(t, key+` = { unknown_future_key = { secret = "SECRET_SENTINEL" } }`, ReadOnly)
			if bytes.Contains(p.ConfigTOML(), []byte("SECRET_SENTINEL")) {
				t.Fatal("managed/UI/other-agent data was copied")
			}
		})
	}
}

func TestProjectRejectsUnsupportedContextWithoutLeaking(t *testing.T) {
	for name, source := range map[string]string{
		"provider": `model_provider = "SECRET_SENTINEL"`,
		"custom_provider": `[model_providers.openai]
base_url = "SECRET_SENTINEL"`,
		"profile": `profile = "SECRET_SENTINEL"`,
		"named_profile": `[profiles.fixture]
model = "SECRET_SENTINEL"`,
		"keyring":              `cli_auth_credentials_store = "keyring"`,
		"auto_auth":            `cli_auth_credentials_store = "auto"`,
		"ephemeral_auth":       `cli_auth_credentials_store = "ephemeral"`,
		"bad_auth_type":        `cli_auth_credentials_store = { token = "SECRET_SENTINEL" }`,
		"endpoint":             `openai_base_url = "SECRET_SENTINEL"`,
		"chatgpt_endpoint":     `chatgpt_base_url = "SECRET_SENTINEL"`,
		"forced_login":         `forced_login_method = "SECRET_SENTINEL"`,
		"forced_workspace":     `forced_chatgpt_workspace_id = "SECRET_SENTINEL"`,
		"auth_command":         `auth_command = "SECRET_SENTINEL"`,
		"auth_environment":     `env_key = "SECRET_SENTINEL"`,
		"source_state_path":    `sqlite_home = "SECRET_SENTINEL"`,
		"arbitrary_key":        `"SECRET_SENTINEL_KEY" = "SECRET_SENTINEL"`,
		"web_search":           `web_search = "live"`,
		"bad_definitions_type": `model_providers = ["SECRET_SENTINEL"]`,
	} {
		t.Run(name, func(t *testing.T) {
			p, err := Project([]byte(source), ReadOnly)
			if p != nil || !errors.Is(err, ErrUnsupportedContext) || err.Error() != "unsupported_context" {
				t.Fatalf("want safe unsupported_context, got %v", err)
			}
		})
	}
	if _, err := Project([]byte(""), Mode("SECRET_SENTINEL")); err != ErrUnsupportedContext {
		t.Fatalf("invalid mode: %v", err)
	}
}

func TestProjectRejectsInvalidModelSettings(t *testing.T) {
	for name, source := range map[string]string{
		"model_type": "model = true", "model_syntax": `model = "SECRET_SENTINEL with spaces"`,
		"review_type": "review_model = 3", "review_syntax": `review_model = "../SECRET_SENTINEL"`,
		"model_too_long": `model = "` + strings.Repeat("a", 129) + `"`,
		"provider_type":  "model_provider = 3", "effort_empty": `model_reasoning_effort = ""`,
		"plan_effort_empty": `plan_mode_reasoning_effort = ""`, "effort_type": "model_reasoning_effort = 2",
		"service_type": "service_tier = [1]", "float_context": "model_context_window = 9007199254740993.0",
		"string_context": `model_context_window = "9007199254740993"`, "bool_context": "model_context_window = true",
		"float_compact": "model_auto_compact_token_limit = 1.0", "string_compact": `model_auto_compact_token_limit = "4"`,
		"scope_enum": `model_auto_compact_token_limit_scope = "SECRET_SENTINEL"`, "scope_type": "model_auto_compact_token_limit_scope = 3",
	} {
		t.Run(name, func(t *testing.T) {
			p, err := Project([]byte(source), ReadOnly)
			if p != nil || err != ErrInvalidModelSetting || err.Error() != "invalid_model_setting" {
				t.Fatalf("want value-free invalid_model_setting, got %v", err)
			}
		})
	}
}

func TestProjectRejectsInvalidTOMLAndBoundsBeforeDecode(t *testing.T) {
	for name, source := range map[string][]byte{
		"duplicate":        []byte("model = 'SECRET_SENTINEL'\nmodel = 'SECRET_SENTINEL'"),
		"dotted_duplicate": []byte("features.hooks = false\n[features]\nhooks = true"),
		"syntax":           []byte(`model = "SECRET_SENTINEL`),
		"integer_overflow": []byte("model_context_window = 9223372036854775808"),
		"invalid_utf8":     {'m', 'o', 'd', 'e', 'l', '=', '"', 0xff, '"'},
	} {
		t.Run(name, func(t *testing.T) {
			if p, err := Project(source, ReadOnly); p != nil || err != ErrInvalidFormat {
				t.Fatalf("want parser errors reduced to invalid_format, got %v", err)
			}
		})
	}
	if _, err := Project(bytes.Repeat([]byte{'x'}, MaxInputBytes+1), ReadOnly); err != ErrInputTooLarge {
		t.Fatalf("oversize must fail before syntax decode: %v", err)
	}
	if _, err := Project(bytes.Repeat([]byte{' '}, MaxInputBytes), ReadOnly); err != nil {
		t.Fatalf("inclusive input bound: %v", err)
	}
}

func TestProjectIntegerLimitsAndOpenStrings(t *testing.T) {
	p := mustProject(t, "model_context_window = 9223372036854775807\nmodel_auto_compact_token_limit = -9223372036854775808\nmodel_reasoning_effort = 'custom advertised effort'\nservice_tier = 'custom advertised tier'\nmodel = ''\nreview_model = 'org/model:v2'", ReadOnly)
	var output map[string]any
	if err := toml.Unmarshal(p.ConfigTOML(), &output); err != nil {
		t.Fatal(err)
	}
	if output["model_context_window"] != int64(9223372036854775807) || output["model_auto_compact_token_limit"] != int64(-9223372036854775808) || output["model_reasoning_effort"] != "custom advertised effort" || output["service_tier"] != "custom advertised tier" || output["model"] != "" {
		t.Fatal("pinned signed integers/open strings/existing model syntax were coerced")
	}
}

func TestProjectionOutputsAreDeterministicCopiesAndConcurrent(t *testing.T) {
	source := []byte(currentSource)
	p, err := Project(source, ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	want := p.ConfigTOML()
	for i := range source {
		source[i] = 'x'
	}
	output := p.ConfigTOML()
	output[0] = 'x'
	overrides := p.ManagedOverrides()
	overrides[0] = "SECRET_SENTINEL"
	if !bytes.Equal(want, p.ConfigTOML()) || p.ManagedOverrides()[0] != "notify=[]" {
		t.Fatal("caller changed private projection state")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 4 {
				fresh := mustProject(t, currentSource, ReadOnly)
				if !bytes.Equal(want, fresh.ConfigTOML()) || !bytes.Equal(want, p.ConfigTOML()) {
					t.Error("nondeterministic output")
				}
				p.ManagedOverrides()[0] = "caller-owned"
			}
		})
	}
	wg.Wait()
}

func TestManagedOverridesMatchProbedAliases(t *testing.T) {
	p := mustProject(t, "", ReadOnly)
	want := []string{"notify=[]", "check_for_update_on_startup=false", `history.persistence="none"`, "analytics.enabled=false", `web_search="disabled"`}
	for _, key := range []string{"codex_hooks", "hooks", "plugin_hooks", "plugins", "apps", "enable_mcp_apps", "multi_agent", "multi_agent_v2", "collab", "browser_use", "computer_use", "memories", "memory_tool", "shell_snapshot", "shell_snapshot_v2", "remote_control", "background_paginated_rollout_migration"} {
		want = append(want, "features."+key+"=false")
	}
	if got := p.ManagedOverrides(); len(got) < len(want) || !reflect.DeepEqual(got[:len(want)], want) {
		t.Fatal("original managed control prefix differs from the retained probe")
	}
	if bytes.Contains(p.ConfigTOML(), []byte("strict")) {
		t.Fatal("strict config launch requirement was invented as a TOML field")
	}
}
