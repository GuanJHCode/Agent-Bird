package host

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodexSystemSkillProjectionRejectsMissingOrForeignData(t *testing.T) {
	valid := func() map[string]any {
		return map[string]any{"data": []any{map[string]any{"cwd": "/work", "errors": []any{}, "skills": []any{map[string]any{"name": "fixture", "scope": "system", "path": "/home/skills/.system/fixture/SKILL.md", "enabled": true}}}}}
	}
	if _, err := codexSystemSkillsProjection(valid(), "/home", "/work"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"missing", "empty", "foreign", "errors"} {
		t.Run(kind, func(t *testing.T) {
			result := valid()
			entry := result["data"].([]any)[0].(map[string]any)
			switch kind {
			case "missing":
				delete(result, "data")
			case "empty":
				entry["skills"] = []any{}
			case "foreign":
				entry["skills"].([]any)[0].(map[string]any)["path"] = "/source/skills/.system/fixture/SKILL.md"
			case "errors":
				entry["errors"] = []any{map[string]any{"message": "fixture"}}
			}
			if _, err := codexSystemSkillsProjection(result, "/home", "/work"); err == nil {
				t.Fatal("unverified system skill catalog accepted")
			}
		})
	}
}

func TestCodexSystemSkillSealDetectsContentAndMembershipChanges(t *testing.T) {
	for _, kind := range []string{"content", "new-file", "marker", "mode"} {
		t.Run(kind, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			source, scratch := filepath.Join(root, "source"), filepath.Join(root, "scratch")
			for _, p := range []string{source, scratch} {
				if err := os.Mkdir(p, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"config.toml", "auth.json"} {
				if err := os.WriteFile(filepath.Join(source, name), []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			home, err := createCodexHome(source, scratch, []byte("model='fixture'"))
			if err != nil {
				t.Fatal(err)
			}
			defer home.detachAuth()
			system := filepath.Join(home.path, "skills/.system")
			if err := os.MkdirAll(filepath.Join(system, "fixture"), 0700); err != nil {
				t.Fatal(err)
			}
			for name, data := range map[string]string{".codex-system-skills.marker": "fixture-fingerprint", "fixture/SKILL.md": "fixture instruction"} {
				if err := os.WriteFile(filepath.Join(system, name), []byte(data), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := home.sealSystemSkills(); err != nil {
				t.Fatal(err)
			}
			var changeErr error
			switch kind {
			case "content":
				changeErr = os.WriteFile(filepath.Join(system, "fixture/SKILL.md"), []byte("changed"), 0600)
			case "new-file":
				changeErr = os.WriteFile(filepath.Join(system, "new"), []byte("new"), 0600)
			case "marker":
				changeErr = os.Remove(filepath.Join(system, ".codex-system-skills.marker"))
			case "mode":
				changeErr = os.Chmod(filepath.Join(system, "fixture/SKILL.md"), 0644)
			}
			if changeErr != nil {
				t.Fatal(changeErr)
			}
			if err := home.verifyInputs(); err == nil {
				t.Fatal("system skill mutation accepted")
			}
		})
	}
}
