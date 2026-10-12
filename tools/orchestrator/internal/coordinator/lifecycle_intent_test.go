package coordinator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestRestartIntentBlocksLostNoticeAndPreservesReleasedIdentity(t *testing.T) {
	state := t.TempDir()
	old := contract.HostHello{LaunchID: "launch", OriginContextID: "origin", HostGeneration: "generation", PID: 10, Birth: "birth", Executable: "/pinned/executable"}
	value := restartIntent{Version: 1, Epoch: 7, Blocked: true, TerminatedHosts: []string{contract.HostIdentitySHA256(old)}}
	if err := writeRestartIntent(state, value); err != nil {
		t.Fatal(err)
	}
	if err := CheckRecoveryAllowed(state, old); err == nil || err.Error() != "coordinator_shutdown" {
		t.Fatalf("lost disposition allowed restart: %v", err)
	}
	if err := clearGlobalRestartBlock(state, 8); err != nil {
		t.Fatal(err)
	}
	if err := CheckRecoveryAllowed(state, old); err == nil {
		t.Fatal("new explicit operation revived the old terminated Host")
	}
	changed := old
	changed.PID, changed.Birth = 20, "authorized-new-birth"
	// This only permits attempting DB/OS authentication, never creates a Host
	// or bypasses rebind/tree-exit proof.
	if err := CheckRecoveryAllowed(state, changed); err != nil {
		t.Fatalf("old process fence rejected a new process before authentication: %v", err)
	}
}

func TestRestartIntentRejectsUnsafeOrNoncanonicalRecords(t *testing.T) {
	for _, kind := range []string{"public-mode", "symlink", "hardlink", "unknown-field", "duplicate-field", "unordered-identities"} {
		t.Run(kind, func(t *testing.T) {
			state := t.TempDir()
			value := restartIntent{Version: 1, Epoch: 7, TerminatedHosts: []string{}}
			if err := writeRestartIntent(state, value); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(state, restartIntentName)
			var err error
			switch kind {
			case "public-mode":
				err = os.Chmod(path, 0644)
			case "symlink":
				err = os.Rename(path, path+".owned")
				if err == nil {
					err = os.Symlink(path+".owned", path)
				}
			case "hardlink":
				err = os.Link(path, path+".alias")
			case "unknown-field":
				err = os.WriteFile(path, []byte("{\"version\":1,\"epoch\":7,\"blocked\":false,\"terminated_hosts\":[],\"extra\":true}\n"), 0600)
			case "duplicate-field":
				err = os.WriteFile(path, []byte("{\"version\":1,\"epoch\":7,\"epoch\":7,\"blocked\":false,\"terminated_hosts\":[]}\n"), 0600)
			case "unordered-identities":
				value.TerminatedHosts = []string{contract.HostIdentitySHA256(contract.HostHello{PID: 1}), contract.HostIdentitySHA256(contract.HostHello{PID: 2})}
				if value.TerminatedHosts[0] < value.TerminatedHosts[1] {
					value.TerminatedHosts[0], value.TerminatedHosts[1] = value.TerminatedHosts[1], value.TerminatedHosts[0]
				}
				body, _ := json.Marshal(value)
				err = os.WriteFile(path, append(body, '\n'), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := CheckRecoveryAllowed(state, contract.HostHello{}); err == nil {
				t.Fatalf("unsafe %s restart intent accepted", kind)
			}
		})
	}
}
