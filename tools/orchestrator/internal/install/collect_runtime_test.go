package install

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCollectPackageNeedsNoPythonAndRetainsPins(t *testing.T) {
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	runtimeRoot := filepath.Join(source, "runtime", "g0")
	if err := os.RemoveAll(runtimeRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runtimeRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeRoot, "runtime-manifest.json"), []byte(`{"version":2,"kind":"collect-only"}`), 0600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "installed")
	result, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: destination, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Runtime.Interpreter != "" {
		t.Fatal("collect-only package depends on Python")
	}
	if err := PinVersion(destination, "task", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallPackage(destination, "1.0.0"); err != ErrVersionPinned {
		t.Fatalf("pin lost: %v", err)
	}
}

func TestCollectRuntimeCannotHideNativePayload(t *testing.T) {
	for _, body := range []string{`{"version":2,"kind":"collect-only","interpreter":{"path":"/usr/bin/python3"}}`, `{"version":1,"kind":"collect-only"}`, `{"version":2,"kind":"native"}`, `{"version":2,"kind":"collect-only","extra":true}`} {
		root := t.TempDir()
		r := filepath.Join(root, "runtime", "g0")
		os.MkdirAll(r, 0700)
		os.WriteFile(filepath.Join(r, "runtime-manifest.json"), []byte(body), 0600)
		if _, err := ValidateRuntimeBundle(root); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	root := t.TempDir()
	r := filepath.Join(root, "runtime", "g0")
	os.MkdirAll(r, 0700)
	os.WriteFile(filepath.Join(r, "runtime-manifest.json"), []byte(`{"version":2,"kind":"collect-only"}`), 0600)
	os.WriteFile(filepath.Join(r, "unexpected.py"), []byte("code"), 0600)
	if _, err := ValidateRuntimeBundle(root); err == nil {
		t.Fatal("accepted extra native payload")
	}
}
