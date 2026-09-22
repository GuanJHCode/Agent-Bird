package host

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/pelletier/go-toml/v2"
)

type codexInput struct {
	path           string
	device, inode  uint64
	size, modified int64
	mode           uint32
	digest         string
}

type codexHome struct {
	path, source, runtime string
	inputs                []codexInput
	snapshots             []codexInput
	constraints           []string
	provisionSystem       bool
	systemPaths           []string
}

// Credentials are only hashed in memory for change detection, never copied.
func codexInputIdentity(path string, private bool) (codexInput, error) {
	fail := func() (codexInput, error) { return codexInput{}, errors.New("codex_input_unverified") }
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fail()
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fail()
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || int(stat.Uid) != os.Getuid() || info.Mode().Perm()&0022 != 0 || (private && info.Mode().Perm() != 0600) || info.Size() > 1024*1024 {
		return fail()
	}
	hash := sha256.New()
	if n, err := io.Copy(hash, io.LimitReader(file, 1024*1024+1)); err != nil || n != info.Size() {
		return fail()
	}
	after, err := file.Stat()
	if err != nil || after.ModTime() != info.ModTime() || after.Size() != info.Size() {
		return fail()
	}
	return codexInput{path: path, device: uint64(stat.Dev), inode: stat.Ino, mode: uint32(info.Mode().Perm()), size: info.Size(), modified: info.ModTime().UnixNano(), digest: hex.EncodeToString(hash.Sum(nil))}, nil
}

func createCodexHome(source, scratch string, snapshot []byte) (result *codexHome, retErr error) {
	for _, path := range []string{source, scratch} {
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil || canonical != path || !filepath.IsAbs(path) {
			return nil, errors.New("codex_home_unverified")
		}
	}
	if len(snapshot) == 0 || len(snapshot) > 1024*1024 {
		return nil, errors.New("codex_config_snapshot_invalid")
	}
	h := &codexHome{path: filepath.Join(scratch, "codex-home"), source: source, runtime: filepath.Join(scratch, "codex-runtime")}
	for _, name := range []string{"config.toml", "auth.json"} {
		input, err := codexInputIdentity(filepath.Join(source, name), name == "auth.json")
		if err != nil {
			return nil, err
		}
		h.inputs = append(h.inputs, input)
	}
	if err := os.Mkdir(h.path, 0700); err != nil {
		return nil, errors.New("codex_home_reuse_forbidden")
	}
	success := false
	defer func() {
		if !success {
			retErr = errors.Join(retErr, h.detachAuth())
		}
	}()
	if err := os.Mkdir(h.runtime, 0700); err != nil {
		return nil, err
	}
	for _, name := range []string{"tmp", "sqlite", "log"} {
		if err := os.Mkdir(filepath.Join(h.runtime, name), 0700); err != nil {
			return nil, err
		}
	}
	if err := os.Symlink(filepath.Join(h.runtime, "tmp"), filepath.Join(h.path, "tmp")); err != nil {
		return nil, err
	}
	if err := os.Symlink(filepath.Join(source, "auth.json"), filepath.Join(h.path, "auth.json")); err != nil {
		return nil, err
	}
	var err error
	snapshot, err = rebaseCodexSkillRules(snapshot, source, h.path)
	if err != nil {
		return nil, err
	}
	for name, data := range map[string][]byte{"config.toml": snapshot, "installation_id": {}} {
		f, err := os.OpenFile(filepath.Join(h.path, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, err
		}
		_, writeErr := f.Write(data)
		if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
			return nil, err
		}
	}
	if err := h.recordSnapshot(filepath.Join(h.path, "config.toml"), snapshot); err != nil {
		return nil, err
	}
	if err := h.captureReferences(snapshot); err != nil {
		return nil, err
	}
	if err := h.copyInstructions(); err != nil {
		return nil, err
	}
	if err := h.verifyInputs(); err != nil {
		return nil, err
	}
	success = true
	return h, nil
}

func (h *codexHome) verifyInputs() error {
	if h.systemPaths != nil {
		paths, err := codexSystemPaths(h.path)
		if err != nil || !slices.Equal(paths, h.systemPaths) {
			return errors.New("codex_system_skills_changed")
		}
	}
	paths, err := codexInstructionPaths(h.source)
	if err != nil || !slices.Equal(paths, h.constraints) {
		return errors.New("codex_source_home_changed")
	}
	for _, before := range h.inputs {
		after, err := codexInputIdentity(before.path, filepath.Base(before.path) == "auth.json")
		if err != nil || before != after {
			return errors.New("codex_source_home_changed")
		}
	}
	for _, before := range h.snapshots {
		after, err := codexSnapshotIdentity(before.path)
		if err != nil || before != after {
			return errors.New("codex_snapshot_changed")
		}
	}
	for name, target := range map[string]string{"auth.json": filepath.Join(h.source, "auth.json"), "tmp": filepath.Join(h.runtime, "tmp")} {
		observed, err := os.Readlink(filepath.Join(h.path, name))
		if err != nil || observed != target {
			return errors.New("codex_snapshot_changed")
		}
	}
	return nil
}

func (h *codexHome) recordSnapshot(path string, data []byte) error {
	input, err := codexSnapshotIdentity(path)
	if err != nil || input.digest != hashBytes(data) {
		return errors.New("codex_snapshot_changed")
	}
	h.snapshots = append(h.snapshots, input)
	return nil
}

// Global instructions and rules are immutable inputs too; omitting them would
// silently drop source constraints when CODEX_HOME changes.
func codexInstructionPaths(source string) ([]string, error) {
	paths := []string{}
	for _, name := range []string{"AGENTS.md", "AGENTS.override.md"} {
		path := filepath.Join(source, name)
		if _, err := os.Lstat(path); err == nil {
			paths = append(paths, path)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	rules := filepath.Join(source, "rules")
	if info, err := os.Lstat(rules); err == nil {
		if !info.IsDir() {
			return nil, errors.New("codex_rules_unverified")
		}
		err = filepath.WalkDir(rules, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("codex_rules_unverified")
			}
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".rules") {
				paths = append(paths, path)
			}
			if len(paths) > 128 {
				return errors.New("codex_rules_too_large")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	skills := filepath.Join(source, "skills")
	if info, err := os.Lstat(skills); err == nil {
		if !info.IsDir() {
			return nil, errors.New("codex_skills_unverified")
		}
		err = filepath.WalkDir(skills, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path == filepath.Join(skills, ".system") {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("codex_skills_unverified")
			}
			if !entry.IsDir() {
				paths = append(paths, path)
			}
			if len(paths) > 128 {
				return errors.New("codex_skills_too_large")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	slices.Sort(paths)
	return paths, nil
}

func (h *codexHome) copyInstructions() error {
	paths, err := codexInstructionPaths(h.source)
	if err != nil {
		return err
	}
	h.constraints = paths
	total := 0
	for _, path := range paths {
		before, err := codexInputIdentity(path, false)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		total += len(data)
		if total > 4*1024*1024 {
			return errors.New("codex_rules_too_large")
		}
		after, err := codexInputIdentity(path, false)
		if err != nil || before != after || hashBytes(data) != before.digest {
			return errors.New("codex_source_home_changed")
		}
		relative, err := filepath.Rel(h.source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(h.path, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if before.mode&0111 != 0 {
			mode = 0700
		}
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(data)
		if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
			return err
		}
		if err := h.recordSnapshot(target, data); err != nil {
			return err
		}
		h.inputs = append(h.inputs, before)
	}
	return nil
}

// These files are consumed by the worker independently of config.toml. Detect
// changes before launch and before accepting any output, just as for auth.
func (h *codexHome) captureReferences(snapshot []byte) error {
	var cfg map[string]any
	if err := toml.Unmarshal(snapshot, &cfg); err != nil {
		return errors.New("codex_config_snapshot_invalid")
	}
	for _, key := range []string{"model_instructions_file", "model_catalog_json", "experimental_compact_prompt_file"} {
		if raw, exists := cfg[key]; exists {
			path, ok := raw.(string)
			if !ok || !filepath.IsAbs(path) {
				return errors.New("codex_reference_unverified")
			}
			input, err := codexInputIdentity(path, false)
			if err != nil {
				return errors.New("codex_reference_unverified")
			}
			h.inputs = append(h.inputs, input)
		}
	}
	return nil
}

func codexSnapshotIdentity(path string) (codexInput, error) {
	input, err := codexInputIdentity(path, false)
	if err != nil || (input.mode != 0600 && input.mode != 0700) {
		return codexInput{}, errors.New("codex_snapshot_changed")
	}
	return input, nil
}

func rebaseCodexSkillRules(snapshot []byte, source, home string) ([]byte, error) {
	var config map[string]any
	if err := toml.Unmarshal(snapshot, &config); err != nil {
		return nil, errors.New("codex_config_snapshot_invalid")
	}
	changed := false
	var rebase func(map[string]any) error
	rebase = func(table map[string]any) error {
		if skills, ok := table["skills"].(map[string]any); ok {
			if entries, ok := skills["config"].([]any); ok {
				for _, raw := range entries {
					entry, ok := raw.(map[string]any)
					if !ok {
						return errors.New("codex_config_snapshot_invalid")
					}
					path, ok := entry["path"].(string)
					if !ok {
						return errors.New("codex_config_snapshot_invalid")
					}
					rel, err := filepath.Rel(filepath.Join(source, "skills"), path)
					if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel) {
						entry["path"] = filepath.Join(home, "skills", rel)
						changed = true
					}
				}
			}
		}
		if profiles, ok := table["profiles"].(map[string]any); ok {
			for _, raw := range profiles {
				if profile, ok := raw.(map[string]any); ok {
					if err := rebase(profile); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	if err := rebase(config); err != nil {
		return nil, err
	}
	if !changed {
		return snapshot, nil
	}
	return toml.Marshal(config)
}
