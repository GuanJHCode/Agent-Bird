package install

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodexGuardRuntimeRequiresReceiptAndUnchangedGuardAndInterpreter(t *testing.T) {
	for _, fault := range []string{"none", "missing-helper", "helper", "receipt", "interpreter", "collect-only"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			source, binary := makePackage(t, root, "1.0.0")
			for _, name := range []string{"auth_guard.py", "codex_exec.py"} {
				if fault == "missing-helper" && name == "codex_exec.py" {
					continue
				}
				path := filepath.Join(source, "lib/trial", name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("# trusted fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if fault == "collect-only" {
				runtime := filepath.Join(source, "runtime/g0")
				if err := os.RemoveAll(runtime); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(runtime, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(runtime, "runtime-manifest.json"), []byte(`{"version":2,"kind":"collect-only"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			dst := filepath.Join(root, "installed")
			installed, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: dst, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
			if err != nil {
				t.Fatal(err)
			}
			executable := filepath.Join(installed.VersionRoot, "bin/codex-orchestrator")
			switch fault {
			case "helper":
				err = os.WriteFile(filepath.Join(installed.VersionRoot, "lib/trial/auth_guard.py"), []byte("changed"), 0600)
			case "receipt":
				err = os.WriteFile(filepath.Join(dst, "manifests/1.0.0.json"), []byte("{}"), 0600)
			case "interpreter":
				err = os.WriteFile(filepath.Join(root, "python-1.0.0"), []byte("changed"), 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			runtime, helper, err := CodexGuardRuntime(executable)
			if fault == "none" {
				if err != nil || helper != filepath.Join(installed.VersionRoot, "lib/trial/codex_exec.py") || runtime.Interpreter == "" {
					t.Fatalf("runtime=%+v helper=%s err=%v", runtime, helper, err)
				}
			} else if err == nil {
				t.Fatalf("accepted %s", fault)
			}
		})
	}
}
