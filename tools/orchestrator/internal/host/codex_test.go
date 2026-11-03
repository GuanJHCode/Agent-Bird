package host

import (
	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexInvocationWithoutPinCannotBypassNativeGuard(t *testing.T) {
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "codex-missing-pin")
	if err != nil {
		t.Fatal(err)
	}
	inv := providerInvocation{launchInvocation: launchInvocation{args: []string{"/never-executed"}}, provider: "codex-cli"}
	grant := contract.LaunchCommand{CommandID: "cmd", ReservationID: "res", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", GrantedActiveMS: 10000}
	if _, err = h.ExecuteLaunch(context.Background(), grant, inv); err == nil || err.Error() != "codex_guard_pin_missing" {
		t.Fatalf("missing pin accepted: %v", err)
	}
}

func TestReportWrappedCodexCannotBypassInstalledNativeGuard(t *testing.T) {
	pin := adapter.BinaryPin{Path: "/private/never-executed-codex", Version: adapter.CodexVersion, SHA256: adapter.CodexSHA256}
	inv, err := adapter.BuildInvocation(adapter.Request{Provider: adapter.ProviderCodex, Binary: pin, CWD: "/private/tmp", Prompt: "read only", Permission: adapter.Permission{Mode: "plan"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, wrapped := range []bool{false, true} {
		h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "codex-guard")
		if err != nil {
			t.Fatal(err)
		}
		var view contract.InvocationView = inv
		if wrapped {
			view = reportInvocation{base: inv, reportPath: "/private/capability.json"}
		}
		grant := contract.LaunchCommand{CommandID: "cmd", ReservationID: "res", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", GrantedActiveMS: 10000}
		_, err = h.ExecuteLaunch(context.Background(), grant, view)
		if err == nil || err.Error() != "codex_guard_package_untrusted" {
			t.Fatalf("wrapped=%t: guard bypassed, got %v", wrapped, err)
		}
		for name := range view.Environment() {
			if strings.Contains(name, "REFRESH") {
				t.Fatal("Go injected refresh override")
			}
		}
	}
}

func TestCodexPolicyBindsScratchAndCurrentLaunchCapability(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(root, "state", "host-spool", "launch", "attempt", "segment", "scratch")
	cap := filepath.Join(root, "state", "host-spool", "launch", ".reports", "own", "capability.json")
	if err = os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(cap), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cap, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	policy, err := codexNativePolicy(scratch, []string{"ORCHESTRATOR_REPORT_CAPABILITY=" + cap})
	if err != nil || !strings.Contains(policy, "report_capability") || !strings.Contains(policy, scratch) {
		t.Fatalf("policy=%s err=%v", policy, err)
	}
	outside := filepath.Join(root, "capability.json")
	if err = os.WriteFile(outside, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = codexNativePolicy(scratch, []string{"ORCHESTRATOR_REPORT_CAPABILITY=" + outside}); err == nil {
		t.Fatal("accepted foreign capability")
	}
}

func TestCodexCleanupNeverFollowsReplacementHome(t *testing.T) {
	root := t.TempDir()
	scratch := filepath.Join(root, "scratch")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(outside, "auth.json")
	if err := os.WriteFile(original, []byte("SYNTHETIC"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(scratch, "codex-home")); err != nil {
		t.Fatal(err)
	}
	cleanupCodexAuth(scratch)
	if _, err := os.Stat(original); err != nil {
		t.Fatal("cleanup followed replacement home")
	}
	if err := os.Remove(filepath.Join(scratch, "codex-home")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(scratch, "codex-home"), 0700); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(scratch, "codex-home/auth.json")
	if err := os.WriteFile(snapshot, []byte("SYNTHETIC"), 0600); err != nil {
		t.Fatal(err)
	}
	cleanupCodexAuth(scratch)
	if _, err := os.Stat(snapshot); !os.IsNotExist(err) {
		t.Fatal("snapshot retained")
	}
}
