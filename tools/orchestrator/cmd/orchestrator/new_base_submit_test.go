package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestNewBasePreparedControlIdentity(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if e := os.Mkdir(state, 0700); e != nil {
		t.Fatal(e)
	}
	request := coordinator.SubmitRequest{RunID: "new-run", ControllerThread: "owner", NewBaseRework: &store.NewBaseReworkSpec{Version: 1, CommandID: "command"}, NewBaseAuthorization: &store.NewBaseAuthorization{ControlToken: "source-secret"}}
	boot, err := writeBootstrap(state, hostBootstrap{Version: 2, Hello: contract.HostHello{LaunchID: "launch", LaunchToken: "host-token"}})
	if err != nil {
		t.Fatal(err)
	}
	control, err := writeControlCapability(state, request.RunID, request.ControllerThread, "new-token", boot, "launch", "host-token")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := publishNewBasePrepared(state, request, 1, control)
	if err != nil || saved != control {
		t.Fatalf("prepared=%s %v", saved, err)
	}
	request.NewBaseAuthorization.ControlToken = "rotated-source-secret"
	replay, err := loadNewBasePrepared(state, request, 1)
	if err != nil || replay != control {
		t.Fatal("auth leaked into canonical preparation", err)
	}
	raw, err := os.ReadFile(newBasePreparedPath(state, request))
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err = json.Unmarshal(raw, &p); err != nil || len(p) != 3 {
		t.Fatalf("prepared metadata=%s %v", raw, err)
	}
	request.PlanRevision++
	if _, err = loadNewBasePrepared(state, request, 1); err == nil {
		t.Fatal("changed request reused preparation")
	}
}
