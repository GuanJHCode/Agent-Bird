package execbridge

import (
	"encoding/base64"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeInstructionSelectionAndBoundResponse(t *testing.T) {
	for _, selected := range []string{"AGENTS.override.md", "AGENTS.md", "TEAM.md"} {
		t.Run(selected, func(t *testing.T) {
			cwd, _ := skillFixture(t)
			names := []string{"AGENTS.override.md", "AGENTS.md", "TEAM.md"}
			present := false
			for _, name := range names {
				present = present || name == selected
				if present {
					if err := os.WriteFile(filepath.Join(cwd, name), []byte("instruction:"+name), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			scope, err := SealSkillRoots(cwd, []string{".git"}, "", "TEAM.md", "TEAM.md")
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range names {
				params := map[string]any{"path": (&url.URL{Scheme: "file", Path: filepath.Join(cwd, name)}).String(), "sandbox": nil}
				if !scope.Allows("fs/getMetadata", params) || scope.Allows("fs/readFile", params) != (name == selected) {
					t.Fatalf("native instruction precedence changed for %s", name)
				}
			}
			path := filepath.Join(cwd, selected)
			encoded := base64.StdEncoding.EncodeToString([]byte("instruction:" + selected))
			for _, tc := range []struct {
				name  string
				frame map[string]any
				ok    bool
			}{
				{"sealed", map[string]any{"result": map[string]any{"dataBase64": encoded}}, true},
				{"changed", map[string]any{"result": map[string]any{"dataBase64": base64.StdEncoding.EncodeToString([]byte("different"))}}, false},
				{"noncanonical", map[string]any{"result": map[string]any{"dataBase64": encoded + "\n"}}, false},
				{"wrong-type", map[string]any{"result": map[string]any{"dataBase64": 12}}, false},
				{"extra-field", map[string]any{"result": map[string]any{"dataBase64": encoded, "extra": false}}, false},
				{"error", map[string]any{"error": map[string]any{"code": -32004}}, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if err := scope.verifyInstructionResponse(path, tc.frame); (err == nil) != tc.ok {
						t.Fatalf("unexpected instruction response: %v", err)
					}
				})
			}
			if err := os.WriteFile(path, []byte("instruction drift"), 0600); err != nil {
				t.Fatal(err)
			}
			if scope.verifyInstructionResponse(path, map[string]any{"result": map[string]any{"dataBase64": encoded}}) == nil {
				t.Fatal("changed on-disk instruction accepted")
			}
		})
	}
}

func TestNativeInstructionRejectsLinkedOrUnsafeFallbackInputs(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory", "writable"} {
		t.Run(kind, func(t *testing.T) {
			cwd, _ := skillFixture(t)
			path := filepath.Join(cwd, "AGENTS.md")
			target := filepath.Join(cwd, "target")
			if err := os.WriteFile(target, []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(target, path)
			case "hardlink":
				err = os.Link(target, path)
			case "directory":
				err = os.Mkdir(path, 0700)
			case "writable":
				err = os.WriteFile(path, []byte("fixture"), 0660)
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "writable" {
				if err := os.Chmod(path, 0660); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := SealSkillRoots(cwd, []string{".git"}); err == nil {
				t.Fatal("unsafe instruction input accepted")
			}
		})
	}
	for _, fallback := range []string{"../outside", "/outside", ".", "..", ".GIT", ".CODEX", "a\\b", "a\nb"} {
		if _, err := instructionNames([]string{fallback}); err == nil {
			t.Fatal("unsafe fallback filename accepted")
		}
	}
}
