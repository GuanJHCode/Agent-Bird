package adapter

import (
	"strings"
	"testing"
)

func baselineRaw(provider Provider) string {
	switch provider {
	case ProviderClaude:
		return "2.1.267 (Claude Code)"
	case ProviderAGY:
		return "1.2.3"
	case ProviderGrok:
		return "grok 1.0.34 (3736acbc8658)"
	case ProviderCodex:
		return CodexVersion
	default:
		return ""
	}
}

func TestVersionWindowMatchesFrozenCatalog(t *testing.T) {
	cases := []struct {
		provider Provider
		raw      string
		code     string
	}{
		{ProviderClaude, "2.1.267 (Claude Code)", ""},
		{ProviderClaude, "2.1.274 (Claude Code)", ""},
		{ProviderClaude, "v2.1.316", ""},
		{ProviderClaude, "2.1.266 (Claude Code)", "cli_version_below_baseline"},
		{ProviderClaude, "2.1.317 (Claude Code)", "cli_version_above_ceiling"},
		{ProviderClaude, "2.1.313 (Claude Code)", "cli_version_not_published"},
		{ProviderClaude, "2.1.316-rc.1", "cli_version_prerelease"},
		{ProviderClaude, "2.1.316+dev", "cli_version_prerelease"},
		{ProviderClaude, "2.1.316+nightly", "cli_version_prerelease"},
		{ProviderClaude, "claude 2.1.316+dev", "cli_version_prerelease"},
		{ProviderClaude, "2.1.316 (dev)", "cli_version_prerelease"},
		{ProviderClaude, "2.1.316 (nightly.20261102)", "cli_version_prerelease"},
		{ProviderClaude, "2.1.316 (alpha.1)", "cli_version_prerelease"},
		{ProviderClaude, "2.1.316 (dev build abc)", "cli_version_prerelease"},
		{ProviderClaude, "2.1.316 (custom channel)", "cli_version_unrecognized_note"},
		{ProviderAGY, "1.4.12 (build)", "cli_version_unrecognized_note"},
		{ProviderClaude, "fixture-v1", "cli_version_unparsed"},
		{ProviderClaude, "codex-cli 2.1.316", "cli_version_product_mismatch"},
		{ProviderAGY, "1.2.3", ""},
		{ProviderAGY, "1.3.2", ""},
		{ProviderAGY, "agy 1.4.12", ""},
		{ProviderAGY, "1.2.2", "cli_version_below_baseline"},
		{ProviderAGY, "1.4.13", "cli_version_above_ceiling"},
		{ProviderAGY, "1.4.12-nightly.1", "cli_version_prerelease"},
		{ProviderGrok, "grok 1.0.34 (3736acbc8658)", ""},
		{ProviderGrok, "grok 1.0.52 (abc123def456) [stable]", ""},
		{ProviderGrok, "grok 1.0.70 (f50904eaee6c)", ""},
		{ProviderGrok, "grok 1.0.30 (04b7ffed98c6) [stable]", "cli_version_below_baseline"},
		{ProviderGrok, "grok 1.0.73 (abc) [alpha]", "cli_version_prerelease"},
		{ProviderGrok, "grok 1.0.70 (nightly)", "cli_version_prerelease"},
		{ProviderGrok, "grok 1.0.71 (abcdef123456)", "cli_version_above_ceiling"},
		{ProviderGrok, "grok future", "cli_version_unparsed"},
		{ProviderCodex, "codex-cli 0.154.0", ""},
		{ProviderCodex, "codex-cli 0.159.2", ""},
		{ProviderCodex, "0.166.0", ""},
		{ProviderCodex, "codex-cli 0.153.4", "cli_version_below_baseline"},
		{ProviderCodex, "codex-cli 0.167.0", "cli_version_above_ceiling"},
		{ProviderCodex, "codex-cli 0.166.0-alpha.6", "cli_version_prerelease"},
		{ProviderCodex, "codex-cli 0.166.0+dev", "cli_version_prerelease"},
		{ProviderCodex, "codex-cli 0.160.0 [nightly]", "cli_version_prerelease"},
		{ProviderCodex, "latest", "cli_version_unparsed"},
	}
	for _, tc := range cases {
		view := DescribeVersion(tc.provider, tc.raw)
		if tc.code == "" {
			if !view.Admitted || view.Protocol != ProtocolID(tc.provider) || view.Baseline == "" || view.Ceiling == "" {
				t.Fatalf("%s %q view=%+v", tc.provider, tc.raw, view)
			}
			continue
		}
		err := AdmitVersion(tc.provider, tc.raw)
		if err == nil || err.Error() != tc.code || view.Admitted || view.Reason != tc.code {
			t.Fatalf("%s %q err=%v view=%+v", tc.provider, tc.raw, err, view)
		}
		var typed *VersionError
		if !errorsAs(err, &typed) || typed.Baseline == "" || typed.Ceiling == "" {
			t.Fatalf("missing window: %#v", err)
		}
	}
}

func errorsAs(err error, target **VersionError) bool {
	typed, ok := err.(*VersionError)
	if ok {
		*target = typed
	}
	return ok
}

func TestMiddleReleaseReusesBaselineInvocation(t *testing.T) {
	for _, provider := range []Provider{ProviderClaude, ProviderAGY, ProviderGrok} {
		base := invocationArgs(t, provider, baselineRaw(provider))
		versions := PublishedVersions(provider)
		middle := versions[len(versions)/2]
		raw := middle
		if provider == ProviderGrok {
			raw = "grok " + middle + " (abcdef123456)"
		}
		if provider == ProviderClaude {
			raw = middle + " (Claude Code)"
		}
		got := invocationArgs(t, provider, raw)
		if strings.Join(base, "\n") != strings.Join(got, "\n") {
			t.Fatalf("%s middle argv changed\nbase %v\nmiddle %v", provider, base, got)
		}
	}
}

func invocationArgs(t *testing.T, provider Provider, raw string) []string {
	t.Helper()
	pin := pinForTest()
	pin.Version = raw
	req := Request{Provider: provider, Binary: pin, CWD: "/private/workspace", Prompt: "inspect", Permission: Permission{Mode: "plan"}}
	if provider != ProviderCodex {
		req.Profile = &ExecutionProfile{Version: 1, Role: Reviewer, Permission: ReadOnly, TimeoutMS: 1000}
		if provider == ProviderGrok {
			req.Profile.GrokSessionWrite = true
		}
		req.Lock = &ProviderLock{Version: 1, Provider: provider, Protocol: ProtocolID(provider), Binary: pin}
	}
	invocation, err := BuildInvocation(req)
	if err != nil {
		t.Fatal(err)
	}
	args := invocation.Args()
	args[0] = "binary"
	return args
}

func TestCodexLaunchKeepsTrialGuardAndOwnDigest(t *testing.T) {
	pin := codexPinForTest()
	pin.Version = "codex-cli 0.160.0"
	pin.SHA256 = strings.Repeat("b", 64)
	invocation, err := BuildInvocation(Request{Provider: ProviderCodex, Binary: pin, CWD: "/private/workspace", Prompt: "inspect", Permission: Permission{Mode: "plan"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(invocation.Args(), " "), "--model gpt-5.6-luna") {
		t.Fatal("version window changed the pinned model")
	}
	req := Request{Provider: ProviderCodex, Binary: pin, CWD: "/private/workspace", Prompt: "inspect", Permission: Permission{Mode: "plan"}, Profile: &ExecutionProfile{Version: 1, Role: Reviewer, Permission: ReadOnly, TimeoutMS: 1000}, Lock: &ProviderLock{Version: 1, Provider: ProviderCodex, Protocol: ProtocolID(ProviderCodex), Binary: pin}}
	if _, err = BuildInvocation(req); err == nil || err.Error() != "codex_trial_guard_not_ready" {
		t.Fatalf("in-range Codex profile err=%v", err)
	}
	if err = ValidatePin(pin, "0.160.0", pin.SHA256); err == nil || err.Error() != "binary_version_mismatch" {
		t.Fatalf("normalized text replaced raw identity: %v", err)
	}
}

func TestNonVersionGuardsStayClosed(t *testing.T) {
	resume := profileRequest(t, `{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":9000}`)
	resume.Session = SessionRef{Kind: SessionID, ID: "session-1234"}
	if _, err := BuildInvocation(resume); err == nil || err.Error() != "profile_resume_not_verified" {
		t.Fatalf("resume err=%v", err)
	}
	agy := profileRequest(t, `{"version":1,"role":"implementer","permission":"workspace-write","timeout_ms":9000}`)
	agy.Provider = ProviderAGY
	agy.Binary.Version = baselineRaw(ProviderAGY)
	agy.Workspace = &CandidateWorkspace{Version: 1}
	agy.Lock.Provider, agy.Lock.Protocol, agy.Lock.Binary = agy.Provider, ProtocolID(agy.Provider), agy.Binary
	if _, err := BuildInvocation(agy); err == nil || err.Error() != "implementer_unsupported" {
		t.Fatalf("implementer err=%v", err)
	}
}
