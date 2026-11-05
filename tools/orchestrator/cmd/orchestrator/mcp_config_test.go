package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
)

func TestMCPConfigureUsesPrivateTaskScope(t *testing.T) {
	base := t.TempDir()
	t.Setenv("OPENAI_FUTURE_AUTH", "public-config-secret-sentinel")
	t.Setenv("ANTHROPIC_BASE_URL", "https://public-config.invalid")
	t.Setenv("CLAUDE_FUTURE_AUTH", "")
	for _, name := range sourceAuthRouteFixtureNames {
		t.Setenv(name, "public-route-"+name)
	}
	t.Setenv(managedOwnerRequestEnv, filepath.Join(base, "not-an-owner.json"))
	configRoot := filepath.Join(base, "private mcp")
	request := filepath.Join(base, "configure.json")
	raw, _ := json.Marshal(map[string]any{"version": 1, "root": configRoot, "state_dir": filepath.Join(base, "state"), "workspace": base, "providers": map[string]string{}})
	if err := os.WriteFile(request, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if err := run(context.Background(), []string{"mcp", "configure", "--request", request}, &out, &stderr); err != nil {
		t.Fatalf("task-local configure: %v (%s)", err, stderr.String())
	}
	for path, mode := range map[string]os.FileMode{configRoot: 0700, filepath.Join(configRoot, "config.json"): 0600, filepath.Join(configRoot, "mcp.json"): 0600} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("private artifact %s: %v %v", path, info, err)
		}
	}
	config, err := os.ReadFile(filepath.Join(configRoot, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]json.RawMessage
	if err = json.Unmarshal(config, &value); err != nil {
		t.Fatal(err)
	}
	var namespace string
	if err = json.Unmarshal(value["namespace"], &namespace); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(base, "state", "mcp-keys", namespace, "token-key")
	info, err := os.Lstat(keyPath)
	if err != nil || info.Mode().Perm() != 0600 || info.Size() != 32 {
		t.Fatalf("runtime-private key: %v %v", info, err)
	}
	if _, err = os.Lstat(filepath.Join(configRoot, "token-key")); !os.IsNotExist(err) {
		t.Fatal("token authority outside protected runtime root")
	}
	if _, ok := value["owner"]; ok {
		t.Fatal("configuration must not bind a future parent owner")
	}
	if _, ok := value["control_file"]; ok {
		t.Fatal("configuration cannot supply a task control capability")
	}
	var client struct {
		Servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	data, _ := os.ReadFile(filepath.Join(configRoot, "mcp.json"))
	if err = json.Unmarshal(data, &client); err != nil {
		t.Fatal(err)
	}
	server := client.Servers["agent_bird"]
	if !filepath.IsAbs(server.Command) || len(server.Args) != 4 || server.Args[0] != "mcp" || server.Args[1] != "serve" || server.Args[2] != "--config" || server.Args[3] != filepath.Join(configRoot, "config.json") {
		t.Fatalf("task-local stdio entry: %+v", server)
	}
	data, err = os.ReadFile(filepath.Join(configRoot, "codex-args.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range sourceAuthRouteFixtureNames {
		if !bytes.Contains(data, []byte(`\"`+name+`\"`)) {
			t.Fatalf("configuration omitted route name %s", name)
		}
		if bytes.Contains(data, []byte("public-route-"+name)) {
			t.Fatal("configuration embedded route source value")
		}
	}
	var args []string
	if err = json.Unmarshal(data, &args); err != nil {
		t.Fatal(err)
	}
	_, names := sourceAuthEnvironmentProjection(os.Environ())
	names = append(names, managedOwnerRequestEnv)
	sort.Strings(names)
	namesJSON, _ := json.Marshal(names)
	if len(args) != 6 || args[4] != "-c" || args[5] != "mcp_servers.agent_bird.env_vars="+string(namesJSON) {
		t.Fatalf("missing scoped owner request forwarding: %v", args)
	}
	if bytes.Contains(data, []byte("public-config-secret-sentinel")) || bytes.Contains(data, []byte("https://public-config.invalid")) {
		t.Fatal("configuration embedded source environment values")
	}
	if bytes.Contains(data, []byte("not-an-owner.json")) {
		t.Fatal("configuration embedded a future owner request value")
	}
}

func TestDispatchExplicitReasoningPreservesLegacyDefault(t *testing.T) {
	raw := []byte(`{"version":2,"request_id":"effort","provider":"codex","provider_lock":"/lock","directory":"/workspace","prompt":"review","role":"reviewer","acceptance":["read only"],"model":"gpt-6-luna","reasoning":"low","max_attempts":1,"max_active_ms_per_segment":60000,"max_active_ms_total":90000}`)
	var request dispatchRequest
	if err := decode(raw, &request); err != nil {
		t.Fatal(err)
	}
	if err := validateDispatch(request); err != nil {
		t.Fatal(err)
	}
	plan, err := dispatchPlan(request, adapter.ProviderLock{}, "run", "")
	if err != nil {
		t.Fatal(err)
	}
	var payload adapter.InvocationPayload
	if err = json.Unmarshal(plan.Tasks[0].AdapterPayload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Profile.Reasoning != "low" || payload.Profile.Model != "gpt-6-luna" {
		t.Fatalf("explicit minimum profile: %+v", payload.Profile)
	}
	request.Reasoning = ""
	plan, err = dispatchPlan(request, adapter.ProviderLock{}, "run", "")
	if err != nil {
		t.Fatal(err)
	}
	payload = adapter.InvocationPayload{}
	if err = json.Unmarshal(plan.Tasks[0].AdapterPayload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Profile.Reasoning != "" {
		t.Fatal("legacy reasoning default changed")
	}
	encoded, _ := json.Marshal(request)
	if bytes.Contains(encoded, []byte(`"reasoning"`)) {
		t.Fatal("omitted option changed canonical legacy request bytes")
	}
	request.Reasoning = "unbounded"
	if err = validateDispatch(request); err == nil {
		t.Fatal("unsupported reasoning accepted")
	}
}
