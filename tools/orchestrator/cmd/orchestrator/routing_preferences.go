package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/instance"
)

// routingPreferences are advisory metadata for the current owner. They never
// select a provider, start a model, or authorize a dispatch.
type routingPreferences struct {
	Version            int      `json:"version"`
	Mode               string   `json:"mode"`
	PreferredProviders []string `json:"preferred_providers"`
	NativeParallel     bool     `json:"native_parallel"`
}

func defaultRoutingPreferences() routingPreferences {
	return routingPreferences{Version: 1, Mode: "balanced", PreferredProviders: []string{"claude", "grok", "agy"}, NativeParallel: false}
}

func routingPreferencesEqual(a, b routingPreferences) bool {
	if a.Version != b.Version || a.Mode != b.Mode || a.NativeParallel != b.NativeParallel || len(a.PreferredProviders) != len(b.PreferredProviders) {
		return false
	}
	for i := range a.PreferredProviders {
		if a.PreferredProviders[i] != b.PreferredProviders[i] {
			return false
		}
	}
	return true
}

func validateRoutingPreferences(p routingPreferences) error {
	if p.Version != 1 || (p.Mode != "balanced" && p.Mode != "save-primary" && p.Mode != "manual") || p.NativeParallel || len(p.PreferredProviders) == 0 || len(p.PreferredProviders) > 4 {
		return codeError("routing_preferences_invalid")
	}
	seen := map[string]bool{}
	for _, provider := range p.PreferredProviders {
		if provider != "claude" && provider != "grok" && provider != "agy" && provider != "codex" || seen[provider] {
			return codeError("routing_preferences_invalid")
		}
		seen[provider] = true
	}
	return nil
}

func routingPreferenceFile(state string, owner coordinator.OwnerBindRequest) (string, error) {
	if !filepath.IsAbs(state) || filepath.Clean(state) != state || owner.ControllerThread == "" || owner.OriginBirth == "" {
		return "", codeError("routing_owner_unavailable")
	}
	sum := sha256.Sum256([]byte(owner.ControllerThread + "\x00" + owner.OriginBirth))
	return filepath.Join(state, "routing", hex.EncodeToString(sum[:]), "preferences.json"), nil
}

func routingPrivateDirectory(path string) error {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return codeError("routing_path_untrusted")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !owned(info) {
		return codeError("routing_path_untrusted")
	}
	return nil
}

func createRoutingPrivateDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	return routingPrivateDirectory(path)
}

func loadRoutingPreferences(state string, owner coordinator.OwnerBindRequest) (routingPreferences, error) {
	path, err := routingPreferenceFile(state, owner)
	if err != nil {
		return routingPreferences{}, err
	}
	root, directory := filepath.Dir(filepath.Dir(path)), filepath.Dir(path)
	if _, err = os.Lstat(root); os.IsNotExist(err) {
		return defaultRoutingPreferences(), nil
	} else if err != nil {
		return routingPreferences{}, err
	}
	if err = routingPrivateDirectory(root); err != nil {
		return routingPreferences{}, err
	}
	if _, err = os.Lstat(directory); os.IsNotExist(err) {
		return defaultRoutingPreferences(), nil
	} else if err != nil {
		return routingPreferences{}, err
	}
	if err = privateHandleParent(path); err != nil {
		return routingPreferences{}, err
	}
	if _, err = os.Lstat(path); os.IsNotExist(err) {
		return defaultRoutingPreferences(), nil
	} else if err != nil {
		return routingPreferences{}, err
	}
	var preferences routingPreferences
	if err = readPrivateJSON(path, &preferences); err != nil {
		return routingPreferences{}, err
	}
	if err = validateRoutingPreferences(preferences); err != nil {
		return routingPreferences{}, err
	}
	return preferences, nil
}

func storeRoutingPreferences(state string, owner coordinator.OwnerBindRequest, preferences routingPreferences) error {
	if err := validateRoutingPreferences(preferences); err != nil {
		return err
	}
	path, err := routingPreferenceFile(state, owner)
	if err != nil {
		return err
	}
	root, directory := filepath.Dir(filepath.Dir(path)), filepath.Dir(path)
	if err = createRoutingPrivateDirectory(root); err != nil {
		return err
	}
	if err = createRoutingPrivateDirectory(directory); err != nil {
		return err
	}
	if _, err = os.Lstat(path); os.IsNotExist(err) {
		return writeExclusiveJSON(path, preferences)
	} else if err != nil {
		return err
	}
	return replacePrivateJSON(path, preferences)
}

func routingEntry(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || (args[0] != "status" && args[0] != "set") {
		return codeError("invalid_args")
	}
	action := args[0]
	fs := flag.NewFlagSet("routing "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "private runtime state directory")
	request := fs.String("request", "", "private routing preferences JSON")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || (action == "set" && *request == "") || (action == "status" && *request != "") {
		return codeError("invalid_args")
	}
	owner, err := currentOwner(ctx)
	if err != nil {
		return err
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	if err = instance.PrepareStateDir(state); err != nil {
		return err
	}
	state, err = filepath.EvalSymlinks(state)
	if err != nil {
		return err
	}
	// This is the existing coordinator peer/worker-recursion guard. Routing has
	// no provider control or task RPC after this owner-only validation.
	var bound bytes.Buffer
	if err = ownerBind(ctx, []string{"--current", "--state-dir", state}, &bound); err != nil {
		return err
	}
	var boundOwner struct {
		ControllerThread string `json:"controller_thread"`
		OriginPID        int    `json:"origin_pid"`
		OriginBirth      string `json:"origin_birth"`
	}
	if err = json.Unmarshal(bound.Bytes(), &boundOwner); err != nil || boundOwner.ControllerThread != owner.ControllerThread || boundOwner.OriginPID != owner.OriginPID || boundOwner.OriginBirth != owner.OriginBirth {
		return codeError("routing_owner_mismatch")
	}
	if action == "set" {
		var preferences routingPreferences
		if err = readPrivateJSON(*request, &preferences); err != nil {
			return err
		}
		if err = storeRoutingPreferences(state, owner, preferences); err != nil {
			return err
		}
	}
	preferences, err := loadRoutingPreferences(state, owner)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"version": 1, "preferences": preferences, "scope": "current_owner"})
}
