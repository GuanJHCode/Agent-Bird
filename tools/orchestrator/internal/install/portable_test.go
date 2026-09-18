package install

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPortableInstallFromPublicCacheIsPrivateAndRepeatable(t *testing.T) {
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	os.RemoveAll(filepath.Join(source, "runtime", "g0"))
	os.MkdirAll(filepath.Join(source, "runtime", "g0"), 0755)
	os.WriteFile(filepath.Join(source, "runtime", "g0", "runtime-manifest.json"), []byte(`{"version":2,"kind":"collect-only"}`), 0644)
	os.MkdirAll(filepath.Join(source, "bin"), 0755)
	b, _ := os.ReadFile(binary)
	os.WriteFile(filepath.Join(source, "bin", "codex-orchestrator"), b, 0755)
	files := map[string]string{}
	for _, rel := range []string{".codex-plugin/plugin.json", "runtime/g0/runtime-manifest.json", "bin/codex-orchestrator"} {
		h, err := hashFile(filepath.Join(source, rel))
		if err != nil {
			t.Fatal(err)
		}
		files[rel] = h
	}
	data, _ := json.Marshal(map[string]any{"version": "1.0.0", "files": files})
	os.WriteFile(filepath.Join(source, "portable.json"), data, 0644)
	dest := filepath.Join(root, "installed")
	var installed string
	for i := 0; i < 2; i++ {
		result, err := InstallPortable(source, dest, filepath.Join(root, "state"))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			installed = result
		} else if result != installed {
			t.Fatal("reinstalled at different path")
		}
	}
	info, err := os.Stat(installed)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("not a private executable")
	}
	// A different payload with the same version must not replace an installed runtime.
	os.WriteFile(filepath.Join(source, "bin", "codex-orchestrator"), []byte("changed binary"), 0755)
	if _, err := InstallPortable(source, dest, filepath.Join(root, "state")); err == nil {
		t.Fatal("accepted changed payload")
	}
	actual, _ := os.ReadFile(installed)
	if string(actual) != string(b) {
		t.Fatal("overwrote installed binary")
	}
}

func TestPortableManifestRejectsTraversalBeforeCopy(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	os.Mkdir(source, 0700)
	data, _ := json.Marshal(map[string]any{"version": "1.0.0", "files": map[string]string{"bin/codex-orchestrator": "0000000000000000000000000000000000000000000000000000000000000000", ".codex-plugin/plugin.json": "0000000000000000000000000000000000000000000000000000000000000000", "runtime/g0/runtime-manifest.json": "0000000000000000000000000000000000000000000000000000000000000000", "../../escaped": "0000000000000000000000000000000000000000000000000000000000000000"}})
	os.WriteFile(filepath.Join(source, "portable.json"), data, 0600)
	if _, err := InstallPortable(source, filepath.Join(root, "installed"), filepath.Join(root, "state")); err == nil {
		t.Fatal("accepted escaping manifest")
	}
	if _, err := os.Stat(filepath.Join(root, "escaped")); !os.IsNotExist(err) {
		t.Fatal("wrote outside private stage")
	}
}

func TestPortableInstallWaitsForConcurrentFirstUse(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	files := map[string]string{}
	bodies := map[string]string{"bin/codex-orchestrator": "synthetic executable", ".codex-plugin/plugin.json": `{"name":"codex-orchestrator","version":"1.0.0"}`, "runtime/g0/runtime-manifest.json": `{"version":2,"kind":"collect-only"}`}
	for rel, body := range bodies {
		p := filepath.Join(source, rel)
		os.MkdirAll(filepath.Dir(p), 0700)
		os.WriteFile(p, []byte(body), 0700)
		h, _ := hashFile(p)
		files[rel] = h
	}
	data, _ := json.Marshal(map[string]any{"version": "1.0.0", "files": files})
	os.WriteFile(filepath.Join(source, "portable.json"), data, 0600)
	destination := filepath.Join(root, "installed")
	os.Mkdir(destination, 0700)
	lock, err := acquireInstallLock(destination)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := InstallPortable(source, destination, filepath.Join(root, "state")); done <- err }()
	select {
	case err := <-done:
		releaseInstallLock(lock)
		t.Fatalf("did not wait for competing install: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	releaseInstallLock(lock)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("install did not recover after lock release")
	}
}

func TestPortablePolicyCannotWeakenIsolation(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprint(shared), func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			policy, _ := json.Marshal(map[string]any{"version": 1, "write_workspace": "isolated-worktree", "allow_shared_write": shared, "provider_scope": "codex-session", "model": "cli-default"})
			bodies := map[string]string{"bin/codex-orchestrator": "synthetic executable", ".codex-plugin/plugin.json": `{"name":"codex-orchestrator","version":"1.0.0"}`, "runtime/g0/runtime-manifest.json": `{"version":2,"kind":"collect-only"}`, "defaults.json": string(policy)}
			files := map[string]string{}
			for rel, body := range bodies {
				p := filepath.Join(source, rel)
				os.MkdirAll(filepath.Dir(p), 0700)
				os.WriteFile(p, []byte(body), 0700)
				files[rel], _ = hashFile(p)
			}
			manifest, _ := json.Marshal(map[string]any{"version": "1.0.0", "files": files})
			os.WriteFile(filepath.Join(source, "portable.json"), manifest, 0600)
			_, err := InstallPortable(source, filepath.Join(root, "installed"), filepath.Join(root, "state"))
			if shared {
				if err == nil || err.Error() != "default_policy_invalid" {
					t.Fatalf("weak policy: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
