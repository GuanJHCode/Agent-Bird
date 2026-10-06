package adapter

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"strings"
	"testing"
)

func continuationRequest(t *testing.T, provider Provider) Request {
	t.Helper()
	r := profileRequest(t, `{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":9000}`)
	r.Provider = provider
	r.Lock.Provider = provider
	r.Lock.Protocol = ProtocolID(provider)
	r.Session = SessionRef{Kind: SessionID, ID: "native-session"}
	if provider == ProviderAGY {
		r.Session.Kind = ConversationID
	}
	r.Continuation = &contract.ContinuationRef{Version: 1, SourceKind: "host", Mode: contract.ContinuationQuestion, SourceSegmentID: "source", SourceEventID: "question", SourceEventRevision: 7, SourceEventSHA256: strings.Repeat("b", 64), SessionEventID: "init", AdapterPayloadSHA256: strings.Repeat("c", 64), OwnerContextID: "origin"}
	return r
}

func TestTypedContinuationUsesExactProviderResumeAndRejectsWeakenedRef(t *testing.T) {
	for _, p := range []Provider{ProviderClaude, ProviderAGY} {
		t.Run(string(p), func(t *testing.T) {
			r := continuationRequest(t, p)
			help := "--input-format --output-format --permission-mode --disallowed-tools --resume --json-schema"
			flag := "--resume"
			if p == ProviderAGY {
				help = "--input-format --output-format --mode --conversation --json-schema"
				flag = "--conversation"
			}
			inv, err := BuildInvocation(r)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.Join(inv.Args(), " "), flag+" native-session") {
				t.Fatal("native exact-ID resume missing")
			}
			if err = CheckCapabilities(r, help); err != nil {
				t.Fatal(err)
			}
			if err = CheckCapabilities(r, strings.ReplaceAll(help, flag, flag+"-suffix")); err == nil {
				t.Fatal("resume option substring accepted")
			}
			r.Continuation.Mode = "permission"
			if _, err = BuildInvocation(r); err == nil {
				t.Fatal("permission approval disguised as continuation")
			}
			r = continuationRequest(t, p)
			r.Session = SessionRef{}
			if _, err = BuildInvocation(r); err == nil {
				t.Fatal("trusted reference without native session accepted")
			}
			r = continuationRequest(t, p)
			r.Profile.Role, r.Profile.Permission = Implementer, WorkspaceWrite
			r.Permission.Mode = "default"
			r.Workspace = &CandidateWorkspace{Version: 1, RepoRoot: "/private/repo", BaseOID: strings.Repeat("d", 40), Paths: []string{"file"}}
			r.Continuation.WorkspaceSourceSegmentID = "source"
			if _, err = BuildInvocation(r); err != nil {
				t.Fatalf("bound workspace continuation rejected: %v", err)
			}
		})
	}
}

func TestTypedContinuationReferenceRequiresExplicitProvenance(t *testing.T) {
	for _, tc := range []struct {
		name, mode, source string
		valid              bool
	}{
		{"host_question", "question", "host", true},
		{"report_question", "question", "report", true},
		{"host_interrupted", "interrupted", "host", true},
		{"missing_source", "question", "", false},
		{"unknown_source", "question", "legacy", false},
		{"report_interrupted", "interrupted", "report", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := continuationRequest(t, ProviderAGY)
			r.Continuation.Mode, r.Continuation.SourceKind = tc.mode, tc.source
			_, err := BuildInvocation(r)
			if (err == nil) != tc.valid {
				t.Fatalf("provenance admission %s/%s: %v", tc.mode, tc.source, err)
			}
		})
	}
}
