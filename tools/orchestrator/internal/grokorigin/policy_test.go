//go:build !windows

package grokorigin

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestOriginPolicyProtectsStateAndAllCapabilitySources(t *testing.T) {
	state := originState(t)
	layout, err := newPrivateLayout(state)
	if err != nil {
		t.Fatal(err)
	}
	defer layout.closeFiles()
	defer layout.remove()
	_, workspace := originWorkspace(t)
	denied, err := inspectWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := originSandboxPolicy(layout, denied, "none")
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{"(deny file-write*)", "(deny process-fork)", "(deny network*)", "(deny file-read* (subpath " + strconv.Quote(state) + "))", "(deny file-read* (subpath \"/etc/grok\"))", "(deny file-read* (subpath \"/private/etc/grok\"))"} {
		if !strings.Contains(rules, rule) {
			t.Fatalf("required rule absent: %s", rule)
		}
	}
	for _, source := range denied {
		if !strings.Contains(rules, "(deny file-read* (subpath "+strconv.Quote(source)+"))") {
			t.Fatal("capability source omitted from OS seal")
		}
	}
	for _, path := range []string{layout.home, layout.grok, layout.tmp} {
		if !strings.Contains(rules, "(allow file-read* file-write* (subpath "+strconv.Quote(path)+"))") {
			t.Fatal("private runtime exception missing")
		}
	}
	authenticated, err := originSandboxPolicy(layout, denied, "xai_api_key_env")
	if err != nil || strings.Contains(authenticated, "(deny network*)") {
		t.Fatalf("explicit network profile=%v", err)
	}
	if _, err = originSandboxPolicy(layout, denied, "oauth"); err == nil {
		t.Fatal("unknown mode accepted")
	}
	if _, err = originSandboxPolicy(layout, []string{"relative"}, "none"); err == nil {
		t.Fatal("unsafe policy path accepted")
	}
	huge := make([]string, 10000)
	for i := range huge {
		huge[i] = "/private/oversized"
	}
	if _, err = originSandboxPolicy(layout, huge, "none"); err == nil {
		t.Fatal("unbounded policy accepted")
	}
}
func TestOriginSystemCapabilityInspectionRejectsAllExistingTypes(t *testing.T) {
	for _, kind := range []string{"file", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := originState(t)
			path := filepath.Join(root, "managed_config.toml")
			switch kind {
			case "file":
				if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("missing", path); err != nil {
					t.Fatal(err)
				}
			}
			if err := inspectSystemCapabilities([]string{path}); err == nil {
				t.Fatal("system capability input accepted")
			}
		})
	}
	if err := inspectSystemCapabilities([]string{filepath.Join(originState(t), "missing")}); err != nil {
		t.Fatal(err)
	}
}
