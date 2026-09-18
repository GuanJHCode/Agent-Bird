package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
)

func TestRoutingPreferencesAreIsolatedByOwnerScope(t *testing.T) {
	state := privateTaskTemp(t)
	first := coordinator.OwnerBindRequest{ControllerThread: "owner-a", OriginBirth: "birth-a"}
	second := coordinator.OwnerBindRequest{ControllerThread: "owner-a", OriginBirth: "birth-b"}
	want := routingPreferences{Version: 1, Mode: "save-primary", PreferredProviders: []string{"grok", "claude"}, NativeParallel: false}
	if err := storeRoutingPreferences(state, first, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadRoutingPreferences(state, first)
	if err != nil || !routingPreferencesEqual(got, want) {
		t.Fatalf("first owner preferences = %#v, %v", got, err)
	}
	other, err := loadRoutingPreferences(state, second)
	if err != nil || !routingPreferencesEqual(other, defaultRoutingPreferences()) {
		t.Fatalf("second owner inherited preferences = %#v, %v", other, err)
	}
	firstPath, err := routingPreferenceFile(state, first)
	if err != nil {
		t.Fatal(err)
	}
	secondPath, err := routingPreferenceFile(state, second)
	if err != nil {
		t.Fatal(err)
	}
	if firstPath == secondPath || filepath.Dir(firstPath) == filepath.Dir(secondPath) {
		t.Fatalf("owner scopes share storage: %q %q", firstPath, secondPath)
	}
	info, err := os.Stat(filepath.Dir(firstPath))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("owner directory privacy = %#o, %v", info.Mode().Perm(), err)
	}
	info, err = os.Stat(firstPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("preference privacy = %#o, %v", info.Mode().Perm(), err)
	}
}

func TestRoutingPreferencesRejectUnsafePolicy(t *testing.T) {
	valid := routingPreferences{Version: 1, Mode: "balanced", PreferredProviders: []string{"claude", "grok", "agy"}}
	for name, mutate := range map[string]func(*routingPreferences){
		"unknown mode":         func(p *routingPreferences) { p.Mode = "automatic" },
		"empty providers":      func(p *routingPreferences) { p.PreferredProviders = nil },
		"duplicate provider":   func(p *routingPreferences) { p.PreferredProviders = []string{"claude", "claude"} },
		"unknown provider":     func(p *routingPreferences) { p.PreferredProviders = []string{"other"} },
		"native parallel true": func(p *routingPreferences) { p.NativeParallel = true },
		"unknown version":      func(p *routingPreferences) { p.Version = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			got := valid
			got.PreferredProviders = append([]string(nil), valid.PreferredProviders...)
			mutate(&got)
			if err := validateRoutingPreferences(got); err == nil {
				t.Fatal("unsafe routing policy accepted")
			}
		})
	}
}

func TestRoutingPreferencesRejectUnknownStoredFields(t *testing.T) {
	state := privateTaskTemp(t)
	owner := coordinator.OwnerBindRequest{ControllerThread: "owner", OriginBirth: "birth"}
	path, err := routingPreferenceFile(state, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte(`{"version":1,"mode":"manual","preferred_providers":["agy"],"native_parallel":false,"extra":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = loadRoutingPreferences(state, owner); err == nil {
		t.Fatal("unknown persisted field accepted")
	}
}

func TestRoutingCLIStoresOnlyCurrentManagedOwnerPreference(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "routing-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	preferencesPath := filepath.Join(root, "preferences.json")
	if err := writeExclusiveJSON(preferencesPath, routingPreferences{Version: 1, Mode: "manual", PreferredProviders: []string{"agy"}}); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin", "orchestrator")
	if err := os.MkdirAll(filepath.Dir(bin), 0700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", output, err)
	}
	prepareControllerSkill(t, root)
	setPath, statusPath := filepath.Join(root, "set.json"), filepath.Join(root, "status.json")
	cli := filepath.Join(root, "claude")
	script := "#!/bin/sh\nset -eu\n\"$AGENT_BIRD_COMMAND\" routing set --state-dir \"$ROUTING_PREFERENCES_STATE\" --request \"$ROUTING_PREFERENCES_REQUEST\" > \"$ROUTING_PREFERENCES_SET\"\n\"$AGENT_BIRD_COMMAND\" routing status --state-dir \"$ROUTING_PREFERENCES_STATE\" > \"$ROUTING_PREFERENCES_STATUS\"\n"
	if err := os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	providerMarker := filepath.Join(root, "preferred-provider-ran")
	if err := os.WriteFile(filepath.Join(root, "agy"), []byte("#!/bin/sh\ntouch \""+providerMarker+"\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := os.Stat(filepath.Join(state, "coordinator.pid")); err == nil {
			stopCoordinator(t, state)
		}
	}()
	cmd := exec.Command(bin, "controller", "start", "--provider", "claude", "--state-dir", state, "--skill-path", filepath.Join(root, "skills", "orchestrate", "SKILL.md"))
	cmd.Env = environmentWith(os.Environ(), "PATH", root+":"+os.Getenv("PATH"))
	cmd.Env = environmentWith(cmd.Env, "ROUTING_PREFERENCES_STATE", state)
	cmd.Env = environmentWith(cmd.Env, "ROUTING_PREFERENCES_REQUEST", preferencesPath)
	cmd.Env = environmentWith(cmd.Env, "ROUTING_PREFERENCES_SET", setPath)
	cmd.Env = environmentWith(cmd.Env, "ROUTING_PREFERENCES_STATUS", statusPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("controller: %s %v", output, err)
	}
	for _, path := range []string{setPath, statusPath} {
		output, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Preferences routingPreferences `json:"preferences"`
		}
		if err := json.Unmarshal(output, &response); err != nil || response.Preferences.Mode != "manual" || len(response.Preferences.PreferredProviders) != 1 || response.Preferences.PreferredProviders[0] != "agy" {
			t.Fatalf("unexpected routing response %q: %v", output, err)
		}
	}
	if _, err := os.Stat(providerMarker); !os.IsNotExist(err) {
		t.Fatal("routing activated the preferred provider")
	}
}
