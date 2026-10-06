package install

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Every rejected payload must fail before it creates any destination state.
func TestB07LegacyHookAssetsCannotClaimInstalledSupport(t *testing.T) {
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	os.MkdirAll(filepath.Join(source, "scripts"), 0700)
	os.WriteFile(filepath.Join(source, "scripts", "agent-bird-hook"), []byte("#!/bin/sh\nexit 0\n"), 0700)
	dest := filepath.Join(root, "installed")
	_, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
	if err == nil {
		t.Fatal("unregistered Hook accepted without payload closure")
	}
	if _, err = os.Lstat(dest); !os.IsNotExist(err) {
		t.Fatal("rejection created destination")
	}
}

func TestB07RuntimeRejectsUnregisteredFileBeforeInstall(t *testing.T) {
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	os.WriteFile(filepath.Join(source, "runtime", "g0", "unregistered.py"), []byte("private input"), 0644)
	dest := filepath.Join(root, "installed")
	_, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
	if err == nil {
		t.Fatal("unregistered runtime asset accepted")
	}
	if _, err = os.Lstat(dest); !os.IsNotExist(err) {
		t.Fatal("rejection created destination")
	}
}

func TestB07PortableRejectsHardlinkBeforeInstall(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	os.Mkdir(source, 0700)
	original := filepath.Join(root, "original")
	os.WriteFile(original, []byte(`{"version":"1.0.0","files":{}}`), 0600)
	os.Link(original, filepath.Join(source, "portable.json"))
	if _, err := portableRead(filepath.Join(source, "portable.json"), 1024); err == nil {
		t.Fatal("portable hardlink accepted")
	}
}

func b07Payload(t *testing.T, root string) string {
	t.Helper()
	source := filepath.Join(root, "source")
	paths := payloadPaths("collect-only-hooks")
	for rel, mode := range paths {
		body := []byte("fixture " + rel)
		if rel == ".codex-plugin/plugin.json" {
			body = []byte(`{"name":"codex-orchestrator","version":"1.0.0"}`)
		}
		if rel == "runtime/g0/runtime-manifest.json" {
			body = []byte(`{"version":2,"kind":"collect-only"}`)
		}
		if rel == "defaults.json" {
			body = []byte(`{"version":1,"write_workspace":"isolated-worktree","allow_shared_write":false,"provider_scope":"codex-session","model":"cli-default"}`)
		}
		path := filepath.Join(source, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, mode); err != nil {
			t.Fatal(err)
		}
	}
	b07WritePayload(t, source)
	return source
}
func b07WritePayload(t *testing.T, source string) {
	t.Helper()
	metadata := payloadManifest{SchemaVersion: 1, Kind: "collect-only-hooks"}
	for rel, mode := range payloadPaths(metadata.Kind) {
		digest, err := hashFile(filepath.Join(source, rel))
		if err != nil {
			t.Fatal(err)
		}
		metadata.Files = append(metadata.Files, payloadFile{Path: rel, SHA256: digest, Mode: uint32(mode)})
	}
	body, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, payloadManifestName), body, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestB07PayloadClosedListAndDoctor(t *testing.T) {
	for _, change := range []string{"valid", "missing", "extra", "sha", "mode", "symlink", "hardlink", "directory-mode", "manifest-duplicate", "escape"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			source := b07Payload(t, root)
			hook := filepath.Join(source, "scripts/agent-bird-hook")
			switch change {
			case "missing":
				os.Remove(hook)
			case "extra":
				os.WriteFile(filepath.Join(source, "unknown"), []byte("x"), 0600)
			case "sha":
				os.WriteFile(hook, []byte("changed"), 0700)
			case "mode":
				os.Chmod(hook, 0600)
			case "symlink":
				os.Remove(hook)
				os.Symlink("invoke.sh", hook)
			case "hardlink":
				os.Remove(hook)
				os.Link(filepath.Join(source, "scripts/invoke.sh"), hook)
			case "directory-mode":
				os.Chmod(filepath.Join(source, "scripts"), 0755)
			case "manifest-duplicate":
				p := filepath.Join(source, payloadManifestName)
				body, _ := os.ReadFile(p)
				body = bytes.Replace(body, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1)
				os.WriteFile(p, body, 0600)
			case "escape":
				p := filepath.Join(source, payloadManifestName)
				body, _ := os.ReadFile(p)
				body = bytes.Replace(body, []byte("scripts/agent-bird-hook"), []byte("../agent-bird-hook"), 1)
				os.WriteFile(p, body, 0600)
			}
			dest := filepath.Join(root, "installed")
			result, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
			if change != "valid" {
				if err == nil {
					t.Fatal("invalid payload installed")
				}
				if _, err = os.Stat(dest); !os.IsNotExist(err) {
					t.Fatal("prevalidation changed destination")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			report, err := Doctor(dest)
			if err != nil || !report.Healthy {
				t.Fatalf("doctor %v %+v", err, report)
			}
			os.WriteFile(filepath.Join(result.VersionRoot, "extra"), []byte("x"), 0600)
			report, err = Doctor(dest)
			if err != nil || report.Healthy {
				t.Fatal("doctor accepted added object")
			}
		})
	}
}

func TestB07PortableHookClosurePrivateRepeatable(t *testing.T) {
	root := t.TempDir()
	source := b07Payload(t, root)
	metadata := portableManifest{Version: "1.0.0", PayloadSchema: 1, Files: map[string]string{}}
	for rel := range payloadPaths("collect-only-hooks") {
		metadata.Files[rel], _ = hashFile(filepath.Join(source, rel))
	}
	metadata.Files[payloadManifestName], _ = hashFile(filepath.Join(source, payloadManifestName))
	body, _ := json.Marshal(metadata)
	os.WriteFile(filepath.Join(source, "portable.json"), body, 0600)
	dest := filepath.Join(root, "installed")
	for i := 0; i < 2; i++ {
		binary, err := InstallPortable(source, dest, filepath.Join(root, "state"))
		if err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(binary); err != nil || info.Mode().Perm() != 0700 {
			t.Fatal("binary mode")
		}
	}
	for _, entry := range []string{"agent-bird-hook", "agent-bird-agy-hook"} {
		info, err := os.Stat(filepath.Join(dest, "versions/1.0.0/scripts", entry))
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatal("hook mode")
		}
	}
}

func TestB07LegacySymlinkAndHardlinkPreflight(t *testing.T) {
	for _, change := range []string{"symlink", "hardlink", "ancestor"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			source, binary := makePackage(t, root, "1.0.0")
			dest := filepath.Join(root, "installed")
			if change == "ancestor" {
				alias := filepath.Join(root, "alias")
				os.Symlink(source, alias)
				source = alias
			} else {
				path := filepath.Join(source, ".codex-plugin/hooks/hooks.json")
				os.Remove(path)
				if change == "symlink" {
					os.Symlink("../plugin.json", path)
				} else {
					os.Link(filepath.Join(source, ".codex-plugin/plugin.json"), path)
				}
			}
			if _, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"}); err == nil {
				t.Fatal("legacy unsafe path installed")
			}
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Fatal("unsafe legacy mutated destination")
			}
		})
	}
}

func TestB07NativeInstallKeepsDeclaredModuleModeUnderPrivateUmask(t *testing.T) {
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	old := syscall.Umask(0077)
	defer syscall.Umask(old)
	result, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: filepath.Join(root, "installed"), DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(result.VersionRoot, "runtime/g0/delivery_adapter.py"))
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatal("declared module mode changed")
	}
}

func TestB07ExternalBinaryMismatchRejectsBeforeDestination(t *testing.T) {
	root := t.TempDir()
	source := b07Payload(t, root)
	binary := filepath.Join(root, "other-binary")
	os.WriteFile(binary, []byte("different binary"), 0700)
	dest := filepath.Join(root, "installed")
	if _, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"}); err == nil {
		t.Fatal("mismatched external binary accepted")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("mismatched binary created destination")
	}
}
