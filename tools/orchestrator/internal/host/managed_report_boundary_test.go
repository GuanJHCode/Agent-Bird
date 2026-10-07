package host

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// Pure mode selection: actual BuildInvocation snapshots, no process/probe or
// sandbox call. The Source gate consumes precisely this immutable interface.
func TestManagedReportRegistrationGateUsesImmutableMode(t *testing.T) {
	for _, provider := range []adapter.Provider{adapter.ProviderAGY, adapter.ProviderClaude} {
		for _, kind := range []string{"managed", "candidate_review", "legacy"} {
			t.Run(string(provider)+"/"+kind, func(t *testing.T) {
				pin := adapter.BinaryPin{Path: "/private/owned/provider", Version: "fixture-v1", SHA256: strings.Repeat("a", 64)}
				req := adapter.Request{Provider: provider, Binary: pin, CWD: "/private/owned", Prompt: "inspect", Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000}, Lock: &adapter.ProviderLock{Version: 1, Provider: provider, Protocol: adapter.ProtocolID(provider), Binary: pin}}
				if kind == "candidate_review" {
					req.Action = &adapter.CandidateAction{Version: 1, Operation: "review", SourceTask: "candidate"}
				} else if kind == "legacy" {
					req.Profile, req.Lock = nil, nil
				}
				inv, err := adapter.BuildInvocation(req)
				if err != nil {
					t.Fatal(err)
				}
				want := kind != "managed"
				if reportCapabilityEnabled(inv) != want || reportCapabilityEnabled(reportInvocation{base: inv}) != want {
					t.Fatal("report registration does not follow original immutable completion mode")
				}
				// Mutating the caller's request cannot re-enable the selected mode.
				req.Profile, req.Action = nil, nil
				if reportCapabilityEnabled(inv) != want {
					t.Fatal("caller changed report authority after invocation selection")
				}
			})
		}
	}
}

// Actual OS execution: inherited and wrapped values must be cleared, and the
// empty override must not grant a sandbox exception for a known own cap file.
func TestManagedDirectHostClearsReportAuthorityBeforeSandbox(t *testing.T) {
	for _, provider := range []adapter.Provider{adapter.ProviderAGY, adapter.ProviderClaude} {
		for _, source := range []string{"inherited", "wrapped"} {
			t.Run(string(provider)+"/"+source, func(t *testing.T) {
				root, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				spool := filepath.Join(root, "state", "host-spool", "launch")
				capDir := filepath.Join(spool, ".reports", "cap")
				if err = os.MkdirAll(filepath.Join(capDir, "artifacts"), 0700); err != nil {
					t.Fatal(err)
				}
				capPath := filepath.Join(capDir, "capability.json")
				if err = os.WriteFile(capPath, []byte("synthetic-private-capability"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("ORCHESTRATOR_REPORT_EXECUTABLE", "synthetic-parent-helper")
				t.Setenv("ORCHESTRATOR_REPORT_CAPABILITY", capPath)
				status := "ERROR"
				if provider == adapter.ProviderClaude {
					status = "error_during_execution"
				}
				init := managedNativeInit(provider, "direct-session")
				failure := managedNativeFrame(provider, "direct-session", status, "", "helper authority leaked", "")
				structured := `{"version":1,"kind":"technical_question","text":"Managed helper authority absent"}`
				successStatus := "SUCCESS"
				if provider == adapter.ProviderClaude {
					successStatus = "success"
				}
				success := managedNativeFrame(provider, "direct-session", successStatus, structured, "native commentary", "")
				body := []byte(fmt.Sprintf("#!/bin/sh\nprintf '%%s' %s\nif [ -n \"${ORCHESTRATOR_REPORT_EXECUTABLE:-}\" ] || [ -n \"${ORCHESTRATOR_REPORT_CAPABILITY:-}\" ] || /bin/cat %s >/dev/null 2>/dev/null; then\nprintf '%%s' %s\nelse\nprintf '%%s' %s\nfi\n", shellQuote(init), shellQuote(capPath), shellQuote(failure), shellQuote(success)))
				binary := filepath.Join(root, "owned-direct-native")
				if err = os.WriteFile(binary, body, 0700); err != nil {
					t.Fatal(err)
				}
				pin := adapter.BinaryPin{Path: binary, Version: "owned-fixture-v1", SHA256: hashBytes(body)}
				profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 3000}
				lock := &adapter.ProviderLock{Version: 1, Provider: provider, Protocol: adapter.ProtocolID(provider), Binary: pin}
				payload := adapter.InvocationPayload{Provider: string(provider), Directory: root, Prompt: "inspect", Profile: profile, ProviderLock: lock}
				raw, _ := json.Marshal(payload)
				base, err := adapter.BuildInvocation(adapter.Request{Provider: provider, Binary: pin, CWD: root, Prompt: payload.Prompt, Profile: profile, Lock: lock})
				if err != nil {
					t.Fatal(err)
				}
				var inv contract.InvocationView = base
				if source == "wrapped" {
					inv = reportInvocation{base: base, reportPath: capPath, reportExecutable: "synthetic-wrapper-helper"}
				}
				h, err := NewIPC(spool, "producer")
				if err != nil {
					t.Fatal(err)
				}
				if err = h.BindContinuationOwner("owner"); err != nil {
					t.Fatal(err)
				}
				grant := managedHostGrant(t, h, contract.LaunchCommand{CommandID: "command", ReservationID: "reservation", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1, ExecutionEpoch: 1, GrantedActiveMS: 3000, AdapterPayload: raw})
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				result, runErr := h.ExecuteLaunch(ctx, grant, inv)
				sink := &durableSink{}
				if err = h.PublishAll(ctx, sink, 1); err != nil {
					t.Fatal(err)
				}
				var question, exited *contract.Event
				for i := range sink.events {
					e := &sink.events[i]
					if e.Kind == contract.EventExited {
						exited = e
					}
					if e.Kind == contract.EventQuestion {
						question = e
					}
				}
				if exited == nil || exited.Process == nil || exited.ExitCode == nil || *exited.ExitCode != 0 || exited.ActiveMS <= 0 {
					t.Fatal("direct boundary fixture never reached original Start/Wait/Exited")
				}
				if err = process.ConfirmGroupExited(exited.Process.PGID); err != nil {
					t.Fatal(err)
				}
				t.Logf("direct_managed_report_receipt provider=%s source=%s pgid=%d group_absent=true active_ms=%d status=%s", provider, source, exited.Process.PGID, exited.ActiveMS, result.Status)
				if runErr != nil || result.Status != "waiting_user" || question == nil || question.Artifact == nil {
					t.Fatal("managed inherited/wrapped helper authority or sandbox exception remained")
				}
				text, err := os.ReadFile(question.Artifact.Path)
				if err != nil || string(text) != "Managed helper authority absent" || os.Getenv("ORCHESTRATOR_REPORT_EXECUTABLE") != "synthetic-parent-helper" || os.Getenv("ORCHESTRATOR_REPORT_CAPABILITY") != capPath {
					t.Fatal("child clear modified parent environment or lost exact result")
				}
			})
		}
	}
}
