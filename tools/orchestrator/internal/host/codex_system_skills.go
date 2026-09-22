package host

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func codexSystemPaths(home string) ([]string, error) {
	root := filepath.Join(home, "skills/.system")
	paths := []string{}
	total := int64(0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("codex_system_skills_unverified")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 {
			return errors.New("codex_system_skills_unverified")
		}
		paths = append(paths, path)
		total += info.Size()
		if len(paths) > 256 || total > 8*1024*1024 {
			return errors.New("codex_system_skills_unverified")
		}
		return nil
	})
	if err != nil || len(paths) == 0 {
		return nil, errors.New("codex_system_skills_unverified")
	}
	marker := filepath.Join(root, ".codex-system-skills.marker")
	if !slices.Contains(paths, marker) {
		return nil, errors.New("codex_system_skills_unverified")
	}
	data, err := os.ReadFile(marker)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return nil, errors.New("codex_system_skills_unverified")
	}
	slices.Sort(paths)
	return paths, nil
}

func (h *codexHome) sealSystemSkills() error {
	paths, err := codexSystemPaths(h.path)
	if err != nil {
		return err
	}
	for _, path := range paths {
		before, err := codexInputIdentity(path, false)
		if err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if before.mode&0111 != 0 {
			mode = 0700
		}
		if err := os.Chmod(path, mode); err != nil {
			return err
		}
		input, err := codexSnapshotIdentity(path)
		if err != nil || input.digest != before.digest {
			return errors.New("codex_system_skills_changed")
		}
		h.snapshots = append(h.snapshots, input)
	}
	h.systemPaths = paths
	return h.verifyInputs()
}

// Only a hash leaves this function. Native descriptions and local paths are not
// written to receipts or diagnostic output.
func codexSystemSkillsProjection(result map[string]any, home, cwd string) (string, error) {
	invalid := errors.New("codex_system_skills_unverified")
	data, ok := result["data"].([]any)
	if !ok || len(data) != 1 {
		return "", invalid
	}
	entry, ok := data[0].(map[string]any)
	if !ok || entry["cwd"] != cwd {
		return "", invalid
	}
	failures, ok := entry["errors"].([]any)
	if !ok || len(failures) != 0 {
		return "", invalid
	}
	skills, ok := entry["skills"].([]any)
	if !ok {
		return "", invalid
	}
	projection := map[string]any{}
	root := filepath.Join(home, "skills/.system") + string(os.PathSeparator)
	for _, raw := range skills {
		skill, ok := raw.(map[string]any)
		if !ok {
			return "", invalid
		}
		if skill["scope"] != "system" {
			continue
		}
		path, ok := skill["path"].(string)
		if !ok || !strings.HasPrefix(path, root) || filepath.Clean(path) != path {
			return "", invalid
		}
		if _, exists := projection[path]; exists {
			return "", invalid
		}
		projection[path] = skill
	}
	if len(projection) == 0 {
		return "", invalid
	}
	return hashBytes(mustCodexJSON(projection)), nil
}
