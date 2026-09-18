package adapter

import (
	"strings"
	"testing"
)

func grokProfileRequest(t *testing.T) Request {
	req := profileRequest(t, `{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":9000,"grok_session_write":true}`)
	req.Provider = ProviderGrok
	req.Binary.Version = "grok 1.0.34 (3736acbc8658)"
	req.Lock.Provider, req.Lock.Protocol, req.Lock.Binary = req.Provider, ProtocolID(req.Provider), req.Binary
	return req
}

func TestGrokExplicitReadonlyProfile(t *testing.T) {
	req := grokProfileRequest(t)
	inv, err := BuildInvocation(req)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(inv.Args(), " ")
	for _, want := range []string{"--deny MCPTool(*)", "--tools read_file,list_dir,grep", "--permission-mode plan", "--no-subagents", "--output-format streaming-json", "--disable-web-search"} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %s: %s", want, args)
		}
	}
	if inv.Environment()["GROK_DISABLE_AUTOUPDATER"] != "1" {
		t.Fatal("updater not disabled")
	}
	if strings.Contains(args, "--no-auto-update") {
		t.Fatal("depends on hidden update flag")
	}
	help := "--output-format --permission-mode --no-subagents --disable-web-search --session-id --leader-socket --tools --deny"
	if err := CheckCapabilities(req, help); err != nil {
		t.Fatal(err)
	}
	if err := CheckCapabilities(req, strings.ReplaceAll(help, "--no-subagents", "")); err == nil {
		t.Fatal("missing delegation guard accepted")
	}
}

func TestGrokRequiresExplicitSessionWriteAuthorization(t *testing.T) {
	req := grokProfileRequest(t)
	req.Profile.GrokSessionWrite = false
	if _, err := BuildInvocation(req); err == nil || err.Error() != "grok_session_write_required" {
		t.Fatalf("err=%v", err)
	}
	req = grokProfileRequest(t)
	req.Provider = ProviderClaude
	req.Lock.Provider, req.Lock.Protocol = req.Provider, ProtocolID(req.Provider)
	if _, err := BuildInvocation(req); err == nil {
		t.Fatal("non-Grok accepted Grok write authorization")
	}
	req = grokProfileRequest(t)
	req.Binary.Version = "grok future"
	req.Lock.Binary = req.Binary
	if _, err := BuildInvocation(req); err == nil {
		t.Fatal("unverified version accepted")
	}
}

func TestGrokImplementationRequiresExactEditRuleCapability(t *testing.T) {
	req := grokProfileRequest(t)
	req.Profile.Role, req.Profile.Permission = Implementer, WorkspaceWrite
	req.Permission.Mode = "default"
	req.Workspace = &CandidateWorkspace{Version: 1}
	help := "--output-format --permission-mode --no-subagents --disable-web-search --session-id --leader-socket --tools --deny"
	if err := CheckCapabilities(req, help); err == nil {
		t.Fatal("implementation accepted CLI without edit rule support")
	}
	if err := CheckCapabilities(req, help+" --allow <rule>"); err != nil {
		t.Fatal(err)
	}
	req.Permission.Allow = []string{"Edit"}
	if _, err := BuildInvocation(req); err == nil {
		t.Fatal("legacy permission expansion accepted")
	}
}
