package execbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
)

var errSkillRoots = errors.New("codex_skill_roots_unverified")

type skillNode struct {
	Exists, Directory bool
	Device, Inode     uint64
	Mode, Owner       uint32
	Links             uint64
	Size, Modified    int64
	Digest            string
}

// SkillRoots is a Host-owned immutable input description. It authorizes only
// the pinned native skill/git root metadata and sealed project instructions.
// Skill content scans must finish before tools are enabled.
type SkillRoots struct {
	probes            map[string]skillNode
	roots             []string
	nodes             map[string]skillNode
	documents         map[string]skillNode
	selectedDocuments map[string]bool
}

func skillMetadata(path string) (skillNode, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		// Resolve the nearest existing ancestor too: a missing leaf must not
		// hide a symlink in its parent chain.
		for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
			if _, e := os.Lstat(parent); os.IsNotExist(e) {
				if parent == filepath.Dir(parent) {
					return skillNode{}, errSkillRoots
				}
				continue
			} else if e != nil {
				return skillNode{}, errSkillRoots
			}
			resolved, e := filepath.EvalSymlinks(parent)
			if e != nil || resolved != parent {
				return skillNode{}, errSkillRoots
			}
			return skillNode{}, nil
		}
	}
	if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
		return skillNode{}, errSkillRoots
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return skillNode{}, errSkillRoots
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return skillNode{}, errSkillRoots
	}
	return skillNode{Exists: true, Directory: info.IsDir(), Device: uint64(stat.Dev), Inode: stat.Ino, Mode: uint32(info.Mode()), Owner: stat.Uid, Links: uint64(stat.Nlink), Size: info.Size(), Modified: info.ModTime().UnixNano()}, nil
}

func SealSkillRoots(cwd string, markers []string, fallbackDocuments ...string) (*SkillRoots, error) {
	canonical, err := filepath.EvalSymlinks(cwd)
	if err != nil || canonical != cwd || !filepath.IsAbs(cwd) || filepath.Clean(cwd) != cwd || len(markers) == 0 || len(markers) > 16 {
		return nil, errSkillRoots
	}
	for _, marker := range markers {
		if marker == "" || marker == "." || marker == ".." || strings.ContainsAny(marker, "/\\\x00") {
			return nil, errSkillRoots
		}
	}
	documentNames, err := instructionNames(fallbackDocuments)
	if err != nil {
		return nil, err
	}
	s := &SkillRoots{probes: map[string]skillNode{}, documents: map[string]skillNode{}, selectedDocuments: map[string]bool{}}
	project := cwd
	found := false
	for dir := cwd; ; dir = filepath.Dir(dir) {
		for _, marker := range markers {
			path := filepath.Join(dir, marker)
			node, e := skillMetadata(path)
			if e != nil {
				return nil, e
			}
			s.probes[path] = node
			// Native turn-diff discovery concurrently probes every ancestor,
			// even when an earlier marker exists. Bind all exact probes while
			// keeping skill-content roots limited to the nearest project.
			if node.Exists && !found {
				project, found = dir, true
			}
		}
		if dir == "/" {
			break
		}
	}
	documentBytes := int64(0)
	for dir := cwd; ; dir = filepath.Dir(dir) {
		selected := false
		for _, name := range documentNames {
			path := filepath.Join(dir, name)
			if len(s.documents) >= 512 {
				return nil, errSkillRoots
			}
			node, err := sealedInstruction(path)
			if err != nil {
				return nil, err
			}
			documentBytes += node.Size
			if documentBytes > 8*1024*1024 {
				return nil, errSkillRoots
			}
			if !selected && node.Exists {
				s.selectedDocuments[path], selected = true, true
			}
			s.documents[path] = node
			node.Digest = ""
			s.probes[path] = node
		}
		for _, name := range []string{".agents", ".codex"} {
			root := filepath.Join(dir, name, "skills")
			s.roots = append(s.roots, root)
			node, e := skillMetadata(root)
			if e != nil {
				return nil, e
			}
			if node.Exists && !node.Directory {
				return nil, errSkillRoots
			}
			if name == ".agents" {
				s.probes[root] = node
			}
		}
		if dir == project {
			break
		}
	}
	s.nodes, err = s.readNodes()
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (s *SkillRoots) readNodes() (map[string]skillNode, error) {
	nodes := map[string]skillNode{}
	total := int64(0)
	for _, root := range s.roots {
		rootNode, err := skillMetadata(root)
		if err != nil {
			return nil, err
		}
		nodes[root] = rootNode
		if !rootNode.Exists {
			continue
		}
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || len(nodes) >= 512 {
				return errSkillRoots
			}
			rel, err := filepath.Rel(root, path)
			if err != nil || strings.Count(rel, string(os.PathSeparator)) > 16 {
				return errSkillRoots
			}
			node, err := skillMetadata(path)
			if err != nil {
				return err
			}
			info, err := os.Lstat(path)
			if err != nil {
				return errSkillRoots
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || int(stat.Uid) != os.Getuid() || info.Mode().Perm()&0022 != 0 {
				return errSkillRoots
			}
			if !node.Directory {
				if stat.Nlink != 1 || node.Size > 1024*1024 {
					return errSkillRoots
				}
				total += node.Size
				if total > 8*1024*1024 {
					return errSkillRoots
				}
				fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
				if err != nil {
					return errSkillRoots
				}
				file := os.NewFile(uintptr(fd), path)
				opened, openErr := file.Stat()
				if openErr != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() != node.Size || opened.ModTime().UnixNano() != node.Modified {
					file.Close()
					return errSkillRoots
				}
				hash := sha256.New()
				count, readErr := io.Copy(hash, io.LimitReader(file, 1024*1024+1))
				after, statErr := file.Stat()
				closeErr := file.Close()
				if readErr != nil || statErr != nil || closeErr != nil || count != node.Size || !os.SameFile(info, after) || after.ModTime().UnixNano() != node.Modified || !after.Mode().IsRegular() {
					return errSkillRoots
				}
				node.Digest = hex.EncodeToString(hash.Sum(nil))
			}
			nodes[path] = node
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return nodes, nil
}

func (s *SkillRoots) Verify() error {
	for path, before := range s.probes {
		after, err := skillMetadata(path)
		if err != nil || before != after {
			return errSkillRoots
		}
	}
	for path, before := range s.documents {
		after, err := sealedInstruction(path)
		if err != nil || before != after {
			return errSkillRoots
		}
	}
	nodes, err := s.readNodes()
	if err != nil || !reflect.DeepEqual(nodes, s.nodes) {
		return errSkillRoots
	}
	return nil
}

func (s *SkillRoots) ProtectedDirectories() []string {
	paths := make([]string, 0, len(s.roots))
	for _, root := range s.roots {
		paths = append(paths, filepath.Dir(root))
	}
	sort.Strings(paths)
	return paths
}

func (s *SkillRoots) metadataPath(params map[string]any) (string, bool) {
	// The fixed native caller omits followSymlinks. Do not admit alternate
	// operation semantics or future parameters through the startup exception.
	if len(params) != 2 {
		return "", false
	}
	policy, exists := params["sandbox"]
	if !exists || policy != nil {
		return "", false
	}
	raw, ok := params["path"].(string)
	if !ok {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || u.Host != "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || !filepath.IsAbs(u.Path) || filepath.Clean(u.Path) != u.Path || strings.ContainsRune(u.Path, 0) {
		return "", false
	}
	if raw != (&url.URL{Scheme: "file", Path: u.Path}).String() {
		return "", false
	}
	if _, ok := s.probes[u.Path]; !ok {
		return "", false
	}
	return u.Path, true
}

func (s *SkillRoots) Allows(method string, params map[string]any) bool {
	if s == nil || (method != "fs/getMetadata" && method != "fs/readFile") {
		return false
	}
	path, ok := s.metadataPath(params)
	if !ok {
		return false
	}
	if method == "fs/readFile" {
		before, exists := s.documents[path]
		if !exists || !before.Exists || !s.selectedDocuments[path] {
			return false
		}
		after, err := sealedInstruction(path)
		return err == nil && before == after
	}
	after, err := skillMetadata(path)
	return err == nil && after == s.probes[path]
}

func (s *SkillRoots) verifyResponse(path string, message map[string]any) error {
	want, ok := s.probes[path]
	if !ok {
		return errPolicy
	}
	after, err := skillMetadata(path)
	if err != nil || want != after {
		return errPolicy
	}
	if !want.Exists {
		failure, ok := message["error"].(map[string]any)
		if !ok || message["result"] != nil || failure["code"] != float64(-32004) || failure["data"] != nil {
			return errPolicy
		}
		if message, ok := failure["message"].(string); !ok || message == "" {
			return errPolicy
		}
		for key := range failure {
			if key != "code" && key != "message" && key != "data" {
				return errPolicy
			}
		}
		return nil
	}
	r, ok := message["result"].(map[string]any)
	if !ok || len(r) != 6 || message["error"] != nil || r["isDirectory"] != want.Directory || r["isFile"] != !want.Directory || r["isSymlink"] != false || r["size"] != float64(want.Size) || r["modifiedAtMs"] != float64(want.Modified/1_000_000) {
		return errPolicy
	}
	// The wire fields are i64. Restrict to exactly representable JSON integers;
	// native 0 denotes an unavailable timestamp and is a valid createdAtMs.
	created, ok := r["createdAtMs"].(float64)
	if !ok || created < -9007199254740991 || created > 9007199254740991 || created != float64(int64(created)) {
		return errPolicy
	}
	return nil
}
