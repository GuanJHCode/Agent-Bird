package execbridge

import (
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

func instructionNames(fallback []string) ([]string, error) {
	if len(fallback) > 16 {
		return nil, errSkillRoots
	}
	names := []string{"AGENTS.override.md", "AGENTS.md"}
	seen := map[string]bool{"AGENTS.override.md": true, "AGENTS.md": true}
	for _, name := range fallback {
		if name == "" || seen[name] {
			continue
		}
		if len(name) > 128 || filepath.Base(name) != name || strings.ContainsAny(name, "\\\x00\r\n") || name == "." || name == ".." {
			return nil, errSkillRoots
		}
		seen[name] = true
		for _, reserved := range []string{".git", ".codex", ".agent-bird", ".agents"} {
			if strings.EqualFold(name, reserved) {
				return nil, errSkillRoots
			}
		}
		names = append(names, name)
	}
	return names, nil
}

func sealedInstruction(path string) (skillNode, error) {
	node, err := skillMetadata(path)
	if err != nil || !node.Exists {
		return node, err
	}
	if node.Directory || int(node.Owner) != os.Getuid() || node.Mode&0022 != 0 || node.Links != 1 || node.Size > 256*1024 {
		return skillNode{}, errSkillRoots
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return skillNode{}, errSkillRoots
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return skillNode{}, errSkillRoots
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Dev) != node.Device || stat.Ino != node.Inode || !info.Mode().IsRegular() || stat.Nlink != 1 {
		return skillNode{}, errSkillRoots
	}
	raw, err := io.ReadAll(io.LimitReader(f, 256*1024+1))
	after, statErr := skillMetadata(path)
	if err != nil || statErr != nil || int64(len(raw)) != node.Size || node != after {
		return skillNode{}, errSkillRoots
	}
	node.Digest = journalHash(raw)
	return node, nil
}

func (s *SkillRoots) ProtectedInstructions() []string {
	paths := make([]string, 0, len(s.documents))
	for path := range s.documents {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func (s *SkillRoots) verifyInstructionResponse(path string, message map[string]any) error {
	want, exists := s.documents[path]
	after, err := sealedInstruction(path)
	if !exists || !want.Exists || !s.selectedDocuments[path] || err != nil || want != after || message["error"] != nil {
		return errPolicy
	}
	result, ok := message["result"].(map[string]any)
	if !ok || len(result) != 1 {
		return errPolicy
	}
	encoded, ok := result["dataBase64"].(string)
	if !ok || len(encoded) > 350000 {
		return errPolicy
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(raw) != encoded || int64(len(raw)) != want.Size || journalHash(raw) != want.Digest {
		return errPolicy
	}
	return nil
}
