package host

import (
	"github.com/pelletier/go-toml/v2"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexPrivateHomeSnapshotsConfigAndOnlyLinksAuth(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, scratch := filepath.Join(root, "source"), filepath.Join(root, "scratch")
	for _, p := range []string{source, scratch} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	original := []byte("model='fixture'\n")
	if err := os.WriteFile(filepath.Join(source, "config.toml"), original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "auth.json"), []byte("fixture-auth"), 0600); err != nil {
		t.Fatal(err)
	}
	home, err := createCodexHome(source, scratch, original)
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home.path, "config.toml")
	info, err := os.Lstat(config)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("config is not a private immutable snapshot")
	}
	target, err := os.Readlink(filepath.Join(home.path, "auth.json"))
	if err != nil || target != filepath.Join(source, "auth.json") {
		t.Fatal("auth must be linked, never copied")
	}
	if err := os.WriteFile(filepath.Join(source, "config.toml"), []byte("model='changed'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(config)
	if string(got) != string(original) {
		t.Fatal("snapshot followed later source edit")
	}
	if err := home.verifyInputs(); err == nil {
		t.Fatal("source change was not detected")
	}
	if _, err := createCodexHome(source, scratch, original); err == nil {
		t.Fatal("existing task home reused")
	}
}

func TestCodexHomePreservesGlobalInstructionsAndRules(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	source, scratch := filepath.Join(root, "source"), filepath.Join(root, "scratch")
	for _, p := range []string{source, scratch, filepath.Join(source, "rules")} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{"config.toml": "model='fixture'", "auth.json": "fixture-auth", "AGENTS.md": "Preserve source constraints.", "rules/default.rules": "prefix_rule(pattern=[\"rm\"],decision=\"forbidden\")"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	home, err := createCodexHome(source, scratch, []byte("model='fixture'"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"AGENTS.md", "rules/default.rules"} {
		original, _ := os.ReadFile(filepath.Join(source, name))
		copy, err := os.ReadFile(filepath.Join(home.path, name))
		if err != nil || string(copy) != string(original) {
			t.Fatalf("source instruction/rule omitted: %s", name)
		}
	}
}

func TestCodexHomeDetectsSnapshotAndCredentialAliasTampering(t *testing.T) {
	for _, name := range []string{"snapshot", "auth-alias"} {
		t.Run(name, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			source, scratch := filepath.Join(root, "source"), filepath.Join(root, "scratch")
			for _, p := range []string{source, scratch} {
				_ = os.Mkdir(p, 0700)
			}
			for _, p := range []string{"config.toml", "auth.json"} {
				_ = os.WriteFile(filepath.Join(source, p), []byte("fixture"), 0600)
			}
			home, err := createCodexHome(source, scratch, []byte("model='fixture'"))
			if err != nil {
				t.Fatal(err)
			}
			if name == "snapshot" {
				_ = os.WriteFile(filepath.Join(home.path, "config.toml"), []byte("model='changed'"), 0600)
			} else {
				_ = os.Remove(filepath.Join(home.path, "auth.json"))
				_ = os.Symlink(filepath.Join(source, "config.toml"), filepath.Join(home.path, "auth.json"))
			}
			if err := home.verifyInputs(); err == nil {
				t.Fatal("runtime input tampering was not rejected")
			}
		})
	}
}

func TestCodexHomeDetectsNewSourceConstraints(t *testing.T) {
	for _, name := range []string{"AGENTS.md", "AGENTS.override.md", "rules/new.rules", "rules/nested/new.rules"} {
		t.Run(name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source, scratch := filepath.Join(root, "source"), filepath.Join(root, "scratch")
			for _, p := range []string{source, scratch} {
				if err := os.Mkdir(p, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for name, data := range map[string]string{"config.toml": "model='fixture'", "auth.json": "fixture"} {
				if err := os.WriteFile(filepath.Join(source, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			home, err := createCodexHome(source, scratch, []byte("model='fixture'"))
			if err != nil {
				t.Fatal(err)
			}
			defer home.detachAuth()
			if err := home.verifyInputs(); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(source, name)
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("new constraint"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := home.verifyInputs(); err == nil {
				t.Fatal("new source constraint silently omitted")
			}
		})
	}
}

func TestCodexHomeDetectsChangedReferencedInstructions(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, scratch := filepath.Join(root, "source"), filepath.Join(root, "scratch")
	for _, p := range []string{source, scratch} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	instructions := filepath.Join(root, "instructions.md")
	for name, data := range map[string]string{"config.toml": "model='fixture'", "auth.json": "fixture"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(instructions, []byte("Preserve constraints."), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := []byte("model='fixture'\nmodel_instructions_file='" + instructions + "'\n")
	home, err := createCodexHome(source, scratch, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer home.detachAuth()
	if err := home.verifyInputs(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(instructions, []byte("Changed constraints."), 0600); err != nil {
		t.Fatal(err)
	}
	if err := home.verifyInputs(); err == nil {
		t.Fatal("referenced instructions changed without detection")
	}
}

func TestCodexHomePreservesUserSkillFilesAndDetectsNewSkills(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	source, scratch := filepath.Join(root, "source"), filepath.Join(root, "scratch")
	for _, p := range []string{source, scratch, filepath.Join(source, "skills", "fixture", "scripts")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{"config.toml": "model='fixture'", "auth.json": "fixture", "skills/fixture/SKILL.md": "Keep this skill constraint.", "skills/fixture/scripts/check": "#!/bin/sh\nexit 0\n"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(source, "skills/fixture/scripts/check"), 0700); err != nil {
		t.Fatal(err)
	}
	home, err := createCodexHome(source, scratch, []byte("model='fixture'\n[skills]\nconfig=[{path='"+filepath.Join(source, "skills/fixture/SKILL.md")+"',enabled=false}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer home.detachAuth()
	snapshot, err := os.ReadFile(filepath.Join(home.path, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := toml.Unmarshal(snapshot, &config); err != nil {
		t.Fatal(err)
	}
	rule := config["skills"].(map[string]any)["config"].([]any)[0].(map[string]any)
	if rule["path"] != filepath.Join(home.path, "skills/fixture/SKILL.md") || rule["enabled"] != false {
		t.Fatal("copied skill lost its disable rule")
	}

	data, err := os.ReadFile(filepath.Join(home.path, "skills/fixture/SKILL.md"))
	if err != nil || string(data) != "Keep this skill constraint." {
		t.Fatal("user skill omitted")
	}
	info, err := os.Stat(filepath.Join(home.path, "skills/fixture/scripts/check"))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("skill executable semantics changed")
	}
	if err := home.verifyInputs(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "skills", "new"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "skills/new/SKILL.md"), []byte("New constraints."), 0600); err != nil {
		t.Fatal(err)
	}
	if err := home.verifyInputs(); err == nil {
		t.Fatal("new skill omitted without detection")
	}
}
