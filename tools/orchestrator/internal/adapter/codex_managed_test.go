package adapter

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"reflect"
	"strings"
	"testing"
)

func codexManagedRequestFixture() Request {
	binary := BinaryPin{Path: "/opt/owned/codex", Version: CodexVersion, SHA256: CodexSHA256}
	return Request{Provider: ProviderCodex, Binary: binary, CWD: "/owned/workspace", Prompt: "readonly task", Profile: &ExecutionProfile{Version: 1, Role: Reviewer, Permission: ReadOnly, CLIModelDefault: true, TimeoutMS: 60000}, Lock: &ProviderLock{Version: 1, Provider: ProviderCodex, Protocol: ProtocolID(ProviderCodex), Binary: binary}}
}

func TestManagedCodexConstructorSelectsPrivateBackendWithoutOpeningGeneralGuard(t *testing.T) {
	req := codexManagedRequestFixture()
	inv, err := BuildManagedCodexInvocation(req)
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := ManagedCodexDescriptorFor(inv)
	if !ok || spec.Backend != ManagedCodexBackend || !strings.Contains(spec.Prompt, ManagedTaskInstruction()) || spec.ResumeThreadID != "" || spec.Lock != *req.Lock || inv.TaskCompletionMode() != TaskCompletionV1 || len(inv.Stdin()) != 0 || !reflect.DeepEqual(inv.Args(), []string{req.Binary.Path, "app-server", "--strict-config"}) || inv.WorkingDirectory() != req.CWD || len(inv.Environment()) != 0 {
		t.Fatal("wrong immutable managed backend")
	}
	req.Profile.Role = Implementer
	req.Lock.Protocol = "changed"
	if inv.ExecutionProfile().Role != Reviewer || spec.Lock.Protocol != ProtocolID(ProviderCodex) {
		t.Fatal("request alias mutated immutable invocation")
	}
	profile := inv.ExecutionProfile()
	profile.Role = Implementer
	spec.Prompt = "changed"
	fresh, ok := ManagedCodexDescriptorFor(inv)
	if !ok || fresh.Prompt == "changed" || inv.ExecutionProfile().Role != Reviewer {
		t.Fatal("descriptor/profile view leaked mutation")
	}
	guarded := codexManagedRequestFixture()
	if _, err := BuildInvocation(guarded); err == nil || err.Error() != "codex_trial_guard_not_ready" {
		t.Fatal("general typed guard opened", err)
	}
}

func TestManagedCodexConstructorRejectsContractExpansionAndLegacyDescriptors(t *testing.T) {
	for _, kind := range []string{"provider", "pin", "path", "lock", "missing_profile", "role", "permission", "default_model", "model", "reasoning", "grok_state", "timeout", "action", "workspace", "extra", "allow", "deny", "legacy_session", "empty_prompt", "invalid_utf8", "nul", "large_prompt"} {
		t.Run(kind, func(t *testing.T) {
			req := codexManagedRequestFixture()
			switch kind {
			case "provider":
				req.Provider = ProviderAGY
			case "pin":
				req.Binary.SHA256 = strings.Repeat("0", 64)
			case "path":
				req.Binary.Path = "relative"
			case "lock":
				req.Lock.Protocol = ManagedCodexBackend
			case "missing_profile":
				req.Profile = nil
			case "role":
				req.Profile.Role = Implementer
			case "permission":
				req.Profile.Permission = WorkspaceWrite
			case "default_model":
				req.Profile.CLIModelDefault = false
			case "model":
				req.Profile.Model = "gpt-selected"
			case "reasoning":
				req.Profile.Reasoning = "high"
			case "grok_state":
				req.Profile.GrokSessionWrite = true
			case "timeout":
				req.Profile.TimeoutMS = 0
			case "action":
				req.Action = &CandidateAction{}
			case "workspace":
				req.Workspace = &CandidateWorkspace{}
			case "extra":
				req.ExtraArgs = []string{"--experimental"}
			case "allow":
				req.Permission.Allow = []string{"all"}
			case "deny":
				req.Permission.Deny = []string{"Read"}
			case "legacy_session":
				req.Session = SessionRef{Kind: SessionID, ID: "00000000-0000-0000-0000-000000000000"}
			case "empty_prompt":
				req.Prompt = " "
			case "invalid_utf8":
				req.Prompt = string([]byte{0xff})
			case "nul":
				req.Prompt = "task\x00"
			case "large_prompt":
				req.Prompt = strings.Repeat("x", 65537)
			}
			if _, err := BuildManagedCodexInvocation(req); err == nil {
				t.Fatal("managed contract expanded")
			}
		})
	}
	legacy := codexManagedRequestFixture()
	legacy.Profile = nil
	legacy.Lock = nil
	legacy.Permission.Mode = "plan"
	inv, err := BuildInvocation(legacy)
	if err != nil {
		t.Fatal("legacy protocol baseline", err)
	}
	if _, ok := ManagedCodexDescriptorFor(inv); ok {
		t.Fatal("legacy collector manufactured managed selector")
	}
}

func TestManagedCodexConstructorOnlyAdmitsTrustedQuestionReferenceShape(t *testing.T) {
	req := codexManagedRequestFixture()
	req.Session = SessionRef{Kind: SessionID, ID: "00000000-0000-0000-0000-000000000000"}
	req.Continuation = &contract.ContinuationRef{Version: 1, Mode: contract.ContinuationQuestion, SourceKind: "host", SourceSegmentID: "source", SourceEventID: "question", SourceEventRevision: 1, SourceEventSHA256: strings.Repeat("a", 64), SessionEventID: "session", AdapterPayloadSHA256: strings.Repeat("b", 64), OwnerContextID: "owner"}
	inv, err := BuildManagedCodexInvocation(req)
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := ManagedCodexDescriptorFor(inv)
	if !ok || spec.ResumeThreadID != req.Session.ID {
		t.Fatal("exact thread reference lost")
	}
	for _, kind := range []string{"interrupted", "report", "workspace", "missing_session", "wrong_kind", "noncanonical_thread", "missing_owner", "zero_revision"} {
		t.Run(kind, func(t *testing.T) {
			changed := req
			ref := *req.Continuation
			changed.Continuation = &ref
			switch kind {
			case "interrupted":
				ref.Mode = contract.ContinuationInterrupted
			case "report":
				ref.SourceKind = "report"
			case "workspace":
				ref.WorkspaceSourceSegmentID = "other"
			case "missing_session":
				changed.Session.ID = ""
			case "wrong_kind":
				changed.Session.Kind = ConversationID
			case "noncanonical_thread":
				changed.Session.ID = "opaque-legacy-thread"
			case "missing_owner":
				ref.OwnerContextID = ""
			case "zero_revision":
				ref.SourceEventRevision = 0
			}
			if _, err := BuildManagedCodexInvocation(changed); err == nil {
				t.Fatal("unverified reference shape admitted")
			}
		})
	}
}
