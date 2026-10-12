package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestNewBasePreparedPublicationBarrier(t *testing.T) {
	for _, stage := range []string{"host-bootstrap", "control", "state", "new-base-submissions"} {
		t.Run(stage, func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			request := coordinator.SubmitRequest{RunID: "new-run", ControllerThread: "owner", NewBaseRework: &store.NewBaseReworkSpec{Version: 1, CommandID: "command"}}
			boot, err := writeBootstrap(state, hostBootstrap{Version: 2, Hello: contract.HostHello{LaunchID: "original-launch", LaunchToken: "original-host-token"}})
			if err != nil {
				t.Fatal(err)
			}
			control, err := writeControlCapability(state, request.RunID, request.ControllerThread, "original-control-token", boot, "original-launch", "original-host-token")
			if err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected directory sync failure")
			var calls []string
			syncDir := func(dir string) error {
				calls = append(calls, filepath.Base(dir))
				if filepath.Base(dir) == stage {
					return injected
				}
				return syncNewBaseDirectory(dir)
			}
			identity, err := publishNewBasePreparedWithSync(state, request, 1, control, syncDir)
			if !errors.Is(err, injected) || identity != "" {
				t.Fatalf("failed barrier exposed submit identity: %s %v", identity, err)
			}
			order := []string{"host-bootstrap", "control", "state", "new-base-submissions"}
			for i, s := range order {
				if s == stage {
					if !reflect.DeepEqual(calls, order[:i+1]) {
						t.Fatalf("Sync order=%v", calls)
					}
					break
				}
			}
			marker := newBasePreparedPath(state, request)
			if stage != "new-base-submissions" {
				if _, err = os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("marker published before identity barrier: %v", err)
				}
				if identity, err = publishNewBasePrepared(state, request, 1, control); err != nil || identity != control {
					t.Fatalf("retry replaced identities: %s %v", identity, err)
				}
			} else {
				// Link succeeded, final directory Sync failed. A retry must resync and
				// must never replace the original launch/control identities.
				calls = nil
				if identity, err = loadNewBasePreparedWithSync(state, request, 1, syncDir); !errors.Is(err, injected) || identity != "" {
					t.Fatalf("load bypassed failed barrier: %s %v", identity, err)
				}
				if !reflect.DeepEqual(calls, order) {
					t.Fatalf("reload Sync order=%v", calls)
				}
			}
			if identity, err = loadNewBasePrepared(state, request, 1); err != nil || identity != control {
				t.Fatalf("recovery changed control: %s %v", identity, err)
			}
			cap, err := loadControlCapability(identity)
			if err != nil || cap.HostLaunchID != "original-launch" || cap.HostLaunchToken != "original-host-token" || cap.ControlToken != "original-control-token" {
				t.Fatal("recovery changed original identities", err)
			}
		})
	}
}
