package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMCPDispatchRequiresManagedSourceEnvironment(t *testing.T) {
	f := securityMCP(t)
	t.Setenv(managedOwnerRequestEnv, "")
	_, err := f.dispatch(context.Background(), []byte(`{}`))
	if errorCode(err) != "current_owner_unavailable" {
		t.Fatalf("dispatch bypassed managed source authority: %v", err)
	}
}

func TestSourceAuthEnvironmentProjection(t *testing.T) {
	base := []string{"HOME=/public/home", "OPENAI_API_KEY=public-sentinel", "CODEX_HOME=/public/codex", "ANTHROPIC_AUTH_TOKEN=public-token", "CLAUDE_FUTURE_OPTION=public-option", "HTTPS_PROXY=http://public-proxy", "NODE_EXTRA_CA_CERTS=/public/ca"}
	proof := sourceAuthEnvironment(base)
	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{"complete", base, ""},
		{"order_and_last_wins", append([]string{"HOME=obsolete"}, base...), ""},
		{"non_auth_context", append(append([]string{}, base...), "CODEX_THREAD_ID=public-thread", "UNRELATED=public"), ""},
		{"missing", base[1:], "mcp_source_environment_changed"},
		{"changed", append(append([]string{}, base...), "OPENAI_API_KEY=public-changed"), "mcp_source_environment_changed"},
		{"unknown", append(append([]string{}, base...), "CODEX_FUTURE_AUTH=public"), "mcp_source_environment_changed"},
		{"empty_not_unset", append(append([]string{}, base...), "ANTHROPIC_FUTURE_AUTH="), "mcp_source_environment_changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := errorCode(verifySourceAuthEnvironment(proof, tc.env)); got != tc.want {
				t.Fatalf("error=%s want=%s", got, tc.want)
			}
		})
	}
	for _, name := range []string{"OPENAI_FUTURE", "CODEX_HOME", "CODEX_FUTURE", "ANTHROPIC_FUTURE", "CLAUDE_FUTURE", "HOME", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS"} {
		if !sourceAuthEnvironmentName(name) {
			t.Fatalf("projection omitted %s", name)
		}
	}
	for _, p := range []*sourceAuthEnvironmentProof{nil, {Version: 2, Algorithm: proof.Algorithm, SHA256: proof.SHA256}, {Version: 1, Algorithm: "unknown", SHA256: proof.SHA256}, {Version: 1, Algorithm: proof.Algorithm, SHA256: "short"}, {Version: 1, Algorithm: proof.Algorithm, SHA256: strings.Repeat("z", 64)}} {
		if errorCode(verifySourceAuthEnvironment(p, base)) != "mcp_source_environment_unverified" {
			t.Fatal("accepted unsupported proof")
		}
	}
	if sourceAuthEnvironment([]string{"OPENAI_A=bc"}).SHA256 == sourceAuthEnvironment([]string{"OPENAI_Ab=c"}).SHA256 {
		t.Fatal("ambiguous projection")
	}
}

// Explicit names are independent of the production prefix matcher, including
// route selectors, credentials, regions and account/config locations.
var sourceAuthRouteFixtureNames = []string{
	"GROK_HOME", "XAI_API_KEY", "AGY_CONFIG_DIR", "ANTIGRAVITY_CONFIG_DIR",
	"GEMINI_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "GCLOUD_PROJECT", "GCP_PROJECT", "VERTEX_PROJECT", "CLOUD_ML_REGION",
	"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION",
	"AZURE_API_KEY", "AZURE_AD_TOKEN", "AZURE_ENDPOINT", "FOUNDRY_API_KEY",
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME",
	"GROK_FUTURE_AUTH", "XAI_FUTURE_AUTH", "AGY_FUTURE_AUTH", "ANTIGRAVITY_FUTURE_AUTH",
	"GEMINI_FUTURE_AUTH", "GOOGLE_FUTURE_AUTH", "GCLOUD_FUTURE_AUTH", "GCP_FUTURE_AUTH", "VERTEX_FUTURE_AUTH", "CLOUD_ML_FUTURE_AUTH", "AWS_FUTURE_AUTH", "AZURE_FUTURE_AUTH", "FOUNDRY_FUTURE_AUTH",
}

func TestSourceAuthEnvironmentAdditionalRoutes(t *testing.T) {
	for _, name := range sourceAuthRouteFixtureNames {
		t.Run(name, func(t *testing.T) {
			if !sourceAuthEnvironmentName(name) {
				t.Fatalf("projection omitted %s", name)
			}
			proof := sourceAuthEnvironment([]string{name + "=public-route-sentinel"})
			for _, env := range [][]string{nil, {name + "=public-changed"}} {
				if errorCode(verifySourceAuthEnvironment(proof, env)) != "mcp_source_environment_changed" {
					t.Fatal("route change was not detected")
				}
			}
			if errorCode(verifySourceAuthEnvironment(sourceAuthEnvironment(nil), []string{name + "=public-new"})) != "mcp_source_environment_changed" {
				t.Fatal("new route key was not detected")
			}
		})
	}
}

func TestMCPManagedSourceEnvironmentAdmission(t *testing.T) {
	root, err := os.MkdirTemp("/private/tmp", "bird-source-env-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	bin := deliveryTestBinary(t, filepath.Join(root, "bin", "orchestrator"))
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	prepareControllerSkill(t, root)
	script := `#!` + python + `
import json, os, subprocess, sys
mode = sys.argv[1]
env = dict(os.environ)
proof_path = env["AGENT_BIRD_OWNER_REQUEST"]
proof = json.load(open(proof_path))
assert set(proof["source_auth_env"]) == {"version", "algorithm", "sha256"}
assert "public-sentinel" not in json.dumps(proof)
route_names = json.loads(sys.argv[4])
for name in route_names:
    assert env[name] not in json.dumps(proof)
if mode.startswith("missing:"): env.pop(mode.split(":",1)[1], None)
if mode.startswith("changed:"): env[mode.split(":",1)[1]] = "public-route-changed"
if mode.startswith(("missing:","changed:")):
    assert env["CLAUDE_CODE_USE_BEDROCK"] == "1"
    assert env["CLAUDE_CODE_USE_VERTEX"] == "1"
    assert env["CLAUDE_CODE_USE_FOUNDRY"] == "1"
if mode == "missing": env.pop("OPENAI_API_KEY", None)
if mode == "changed": env["OPENAI_API_KEY"] = "public-changed"
if mode == "unknown": env["CODEX_FUTURE_AUTH"] = "public-new"
if mode == "empty_unset": env.pop("ANTHROPIC_FUTURE_AUTH", None)
if mode in ("missing_proof", "unknown_version", "unknown_algorithm", "bad_digest", "legacy_cli"):
    if mode in ("missing_proof", "legacy_cli"): proof.pop("source_auth_env")
    if mode == "unknown_version": proof["source_auth_env"]["version"] = 2
    if mode == "unknown_algorithm": proof["source_auth_env"]["algorithm"] = "unknown"
    if mode == "bad_digest": proof["source_auth_env"]["sha256"] = "invalid"
    with open(proof_path,"w") as f: json.dump(proof,f)
if mode == "legacy_cli":
    r = subprocess.run([env["AGENT_BIRD_COMMAND"], "owner-bind", "--current", "--state-dir", sys.argv[3]], env=env, capture_output=True)
    assert r.returncode == 0
    print("legacy_cli_accepted")
    sys.exit(0)
p = subprocess.Popen([env["AGENT_BIRD_COMMAND"], "mcp", "serve", "--config", sys.argv[2]], env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
def send(v):
    p.stdin.write(json.dumps(v)+"\n"); p.stdin.flush()
send({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"public-fixture","version":"1"}}})
assert json.loads(p.stdout.readline())["id"] == 1
send({"jsonrpc":"2.0","method":"notifications/initialized"})
send({"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"dispatch","arguments":{"request_id":"source-env","provider":"claude","role":"reviewer","prompt":"public review","acceptance":["read only"],"model":"cli-default","budget":{"per_segment_ms":60000,"total_ms":60000,"max_attempts":1}}}})
r=json.loads(p.stdout.readline())
print(json.dumps(r))
p.stdin.close()
assert p.wait(timeout=10) == 0
`
	cli := filepath.Join(root, "claude")
	if err = os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, want string }{
		{"complete", "mcp_provider_not_configured"}, {"missing", "mcp_source_environment_changed"}, {"changed", "mcp_source_environment_changed"}, {"unknown", "mcp_source_environment_changed"}, {"empty_unset", "mcp_source_environment_changed"}, {"missing_proof", "mcp_source_environment_unverified"}, {"unknown_version", "mcp_source_environment_unverified"}, {"unknown_algorithm", "mcp_source_environment_unverified"}, {"bad_digest", "mcp_source_environment_unverified"}, {"legacy_cli", "legacy_cli_accepted"},
	}
	for _, name := range sourceAuthRouteFixtureNames {
		for _, mutation := range []string{"missing:", "changed:"} {
			cases = append(cases, struct{ name, want string }{mutation + name, "mcp_source_environment_changed"})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := filepath.Join(root, tc.name)
			if err := os.Mkdir(base, 0700); err != nil {
				t.Fatal(err)
			}
			state, config := filepath.Join(base, "state"), filepath.Join(base, "config")
			defer func() {
				if _, e := os.Stat(filepath.Join(state, "coordinator.pid")); e == nil {
					stopCoordinator(t, state)
				}
			}()
			request := filepath.Join(base, "configure.json")
			securityWriteJSON(t, request, map[string]any{"version": 1, "root": config, "state_dir": state, "workspace": base, "providers": map[string]string{}})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			setup := exec.CommandContext(ctx, bin, "mcp", "configure", "--request", request)
			if out, e := setup.CombinedOutput(); e != nil {
				t.Fatalf("configure failed: %v %s", e, out)
			}
			cmd := exec.CommandContext(ctx, bin, "controller", "start", "--provider", "claude", "--state-dir", state, "--skill-path", filepath.Join(root, "skills", "orchestrate", "SKILL.md"), "--", tc.name, filepath.Join(config, "config.json"), state, string(mcpTestJSON(t, sourceAuthRouteFixtureNames)))
			env := []string{}
			for _, entry := range os.Environ() {
				name, _, _ := strings.Cut(entry, "=")
				if !sourceAuthEnvironmentName(name) || name == "HOME" {
					env = append(env, entry)
				}
			}
			env = environmentWith(env, "PATH", root+":"+os.Getenv("PATH"))
			env = append(env, "OPENAI_API_KEY=public-sentinel", "ANTHROPIC_FUTURE_AUTH=", "ANTHROPIC_BASE_URL=https://public.invalid", "ANTHROPIC_API_KEY=public-api", "ANTHROPIC_AUTH_TOKEN=public-token", "CODEX_HOME=/public/codex")
			env = append(env, "CLAUDE_CODE_USE_BEDROCK=1", "CLAUDE_CODE_USE_VERTEX=1", "CLAUDE_CODE_USE_FOUNDRY=1")
			for _, name := range sourceAuthRouteFixtureNames {
				env = append(env, name+"=public-route-"+name)
			}
			cmd.Env = env
			out, e := cmd.CombinedOutput()
			if e != nil || !bytes.Contains(out, []byte(tc.want)) {
				t.Fatalf("admission mode=%s expected=%s failed=%v output=%s", tc.name, tc.want, e, out)
			}
			if strings.HasPrefix(tc.want, "mcp_source_environment_") && !bytes.Contains(out, []byte("reconfigure_mcp_and_restart_managed_controller")) {
				t.Fatal("missing safe recovery instruction")
			}
			for _, name := range sourceAuthRouteFixtureNames {
				if bytes.Contains(out, []byte("public-route-"+name)) {
					t.Fatal("route source value appeared in response")
				}
			}
			for _, sentinel := range []string{"public-sentinel", "public-api", "public-token"} {
				if bytes.Contains(out, []byte(sentinel)) {
					t.Fatal("source value appeared in response")
				}
			}
			if _, e := os.Stat(filepath.Join(state, "dispatch")); !os.IsNotExist(e) {
				t.Fatal("admission created dispatch state")
			}
		})
	}
}
