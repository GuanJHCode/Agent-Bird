package main

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
)

// This acceptance runs only in ROOT's isolated container. The input archive is
// read-only; extraction creates files owned by the actual unprivileged user.
func TestLinuxPortablePackageCheckNative(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("AGENT_BIRD_LINUX_PORTABLE_NATIVE") != "1" {
		t.Skip("requires the explicit offline Linux package acceptance container")
	}
	if os.Geteuid() != 65534 {
		t.Fatal("acceptance must run as nobody")
	}
	root := t.TempDir()
	plugin := filepath.Join(root, "plugin")
	if err := os.Mkdir(plugin, 0700); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open("/input/package.tar")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	r := tar.NewReader(input)
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if filepath.IsAbs(h.Name) || filepath.Clean(h.Name) != h.Name || strings.HasPrefix(h.Name, "../") {
			t.Fatal("unsafe acceptance fixture", h.Name)
		}
		p := filepath.Join(plugin, h.Name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0700); err != nil {
				t.Fatal(err)
			}
		case tar.TypeReg:
			if h.Mode != 0600 && h.Mode != 0700 {
				t.Fatal("unexpected fixture mode", h.Name, h.Mode)
			}
			if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(h.Mode))
			if err != nil {
				t.Fatal(err)
			}
			_, copyErr := io.Copy(f, r)
			closeErr := f.Close()
			if copyErr != nil || closeErr != nil {
				t.Fatal(copyErr, closeErr)
			}
		default:
			t.Fatal("unsupported fixture entry", h.Name, h.Typeflag)
		}
	}
	call := func(destination, state, action string) ([]byte, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/test/codex-orchestrator", "plugin-run", "--plugin-root", plugin, "--install-root", destination, "--state-dir", state, "--", action)
		return cmd.CombinedOutput() // Wait the exact original child on every path.
	}
	destination, state := filepath.Join(root, "install"), filepath.Join(root, "state")
	check := func() string {
		t.Helper()
		body, err := call(destination, state, "check")
		if err != nil {
			t.Fatal("public Linux check", err, string(body))
		}
		var report struct {
			Status  string `json:"status"`
			Binary  string `json:"binary"`
			Runtime string `json:"runtime"`
		}
		if err = json.Unmarshal(body, &report); err != nil || report.Status != "ready" || report.Runtime != "collect-only" || !strings.HasPrefix(report.Binary, destination+"/versions/") {
			t.Fatal("invalid public receipt", err, string(body))
		}
		doctor, err := install.Doctor(destination)
		if err != nil || !doctor.Healthy {
			t.Fatal("independent Doctor", doctor, err)
		}
		return report.Binary
	}
	binary := check()
	before, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	if check() != binary {
		t.Fatal("repeated check changed pinned version")
	}
	after, err := os.Stat(binary)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("repeated check rewrote immutable binary", err)
	}
	for _, dir := range []string{destination, filepath.Dir(filepath.Dir(binary))} {
		s, err := os.Stat(dir)
		if err != nil || s.Mode().Perm() != 0700 || int(s.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() {
			t.Fatal("private installed directory", dir, err)
		}
	}
	if _, err := os.Lstat(state); !os.IsNotExist(err) {
		t.Fatal("collect-only installation check created runtime state", err)
	}
	var portable struct {
		Files map[string]string `json:"files"`
	}
	data, err := os.ReadFile(filepath.Join(plugin, "portable.json"))
	if err != nil || json.Unmarshal(data, &portable) != nil {
		t.Fatal("manifest", err)
	}
	version := filepath.Dir(filepath.Dir(binary))
	for rel, want := range portable.Files {
		p := filepath.Join(version, rel)
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != want {
			t.Fatal("installed payload drift", rel)
		}
		s, err := os.Lstat(p)
		if err != nil || !s.Mode().IsRegular() || int(s.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() {
			t.Fatal("unsafe installed file", rel, err)
		}
		mode := os.FileMode(0600)
		if rel == "bin/codex-orchestrator" || strings.HasPrefix(rel, "scripts/") {
			mode = 0700
		}
		if s.Mode().Perm() != mode {
			t.Fatal("installed mode", rel, s.Mode())
		}
	}
	for _, action := range []string{"controller", "serve", "provider"} {
		d, s := filepath.Join(root, "deny-"+action), filepath.Join(root, "state-"+action)
		body, err := call(d, s, action)
		if err == nil || !strings.Contains(string(body), "portable_action_unsupported") {
			t.Fatal("unsupported action escaped policy", action, err, string(body))
		}
		for _, p := range []string{d, s} {
			if _, err := os.Lstat(p); !os.IsNotExist(err) {
				t.Fatal("action refusal wrote files", p, err)
			}
		}
	}
	for _, kind := range []string{"full-native-hooks", "invalid-payload"} {
		path := filepath.Join(plugin, "payload-manifest.json")
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(original, &m); err != nil {
			t.Fatal(err)
		}
		m["kind"], _ = json.Marshal(kind)
		changed, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, changed, 0600); err != nil {
			t.Fatal(err)
		}
		d := filepath.Join(root, "deny-"+kind)
		body, err := call(d, filepath.Join(root, "state-"+kind), "check")
		if err == nil || !strings.Contains(string(body), "portable_payload_invalid") {
			t.Fatal("invalid package accepted", kind, err, string(body))
		}
		if _, err := os.Lstat(d); !os.IsNotExist(err) {
			t.Fatal("invalid package wrote install", d, err)
		}
		if err = os.WriteFile(path, original, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if check() != binary {
		t.Fatal("failed package validation changed committed installation")
	}
	t.Log("Linux arm64: real packaged CLI check, independent Doctor, payload hashes/modes/uid, immutable repeat, action/package refusals; original child Wait completed")
}
