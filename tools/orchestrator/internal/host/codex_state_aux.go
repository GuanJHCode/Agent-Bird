package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const codexAuxiliaryEntryLimit = 256
const codexAuxiliaryDepthLimit = 32

var codexAuxiliaryNames = [...]string{"home/skills", "home/tmp", "home/thread-writer-locks", "log", "tmp"}

type codexAuxiliaryRoots struct {
	State   codexStateRoot
	Home    codexStateRoot
	Runtime [5]codexStateRoot
}

type codexAuxiliaryState struct {
	Version int    `json:"version"`
	SHA256  string `json:"sha256"`
	Entries int    `json:"entries"`
	Bytes   int64  `json:"bytes"`
}

type codexAuxiliaryControl struct {
	Read              codexStateReadControl
	BeforeGroupVerify func()
}

// All auxiliary details are ephemeral matching material. Only the fixed
// domain-separated aggregate/count/bytes belongs in the protected seal.
type codexAuxiliaryEntry struct {
	Name      string
	Kind      string
	Directory codexStateDirectory
	File      codexStateFile
}

type codexAuxiliaryDirectory struct {
	root     int
	relative string
	children []string
}

type codexAuxiliaryObservation struct {
	Roots       codexAuxiliaryRoots
	Entries     []codexAuxiliaryEntry
	Directories []codexAuxiliaryDirectory
	Parents     map[string]codexStateDirectory
}

func codexOpenAnchoredStateRoot(ctx context.Context, root codexStateRoot) (codexStateFDChain, error) {
	chain, err := codexOpenStateRoot(ctx, root.Path)
	if err != nil {
		return nil, err
	}
	if len(chain) != len(root.Parents) {
		chain.close()
		return nil, errCodexStateFile
	}
	for i, node := range chain {
		if node.identity != root.Parents[i] {
			chain.close()
			return nil, errCodexStateFile
		}
	}
	return chain, nil
}

func codexStateDirectoryNames(node codexStateFDNode, maximum int) ([]string, error) {
	if maximum < 0 || maximum > codexAuxiliaryEntryLimit {
		return nil, errCodexStateFile
	}
	fd, err := unix.Openat(node.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errCodexStateFile
	}
	f := os.NewFile(uintptr(fd), "owned-codex-directory")
	defer f.Close()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || codexDirectoryFromStat(node.identity.Path, stat) != node.identity {
		return nil, errCodexStateFile
	}
	entries, err := f.ReadDir(maximum + 1)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > maximum {
		return nil, errCodexStateFile
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	sort.Strings(names)
	return names, nil
}

func codexCaptureAuxiliaryState(ctx context.Context, roots codexAuxiliaryRoots, control codexAuxiliaryControl) (codexAuxiliaryState, error) {
	return codexCaptureAuxiliaryObserved(ctx, roots, control, nil)
}

func codexCaptureAuxiliaryObserved(ctx context.Context, roots codexAuxiliaryRoots, control codexAuxiliaryControl, retained *codexAuxiliaryObservation) (codexAuxiliaryState, error) {
	var empty codexAuxiliaryState
	if ctx.Err() != nil || control.Read.Remaining == nil || *control.Read.Remaining < 0 || *control.Read.Remaining > codexStateTotalLimit || control.Read.FileLimit < 0 || control.Read.FileLimit > codexStateFileLimit || roots.Home.Path != filepath.Join(roots.State.Path, "home") {
		return empty, errCodexStateFile
	}
	for _, root := range []codexStateRoot{roots.State, roots.Home} {
		chain, err := codexOpenAnchoredStateRoot(ctx, root)
		if err != nil {
			return empty, err
		}
		chain.close()
	}
	for i, root := range roots.Runtime {
		if root.Path != filepath.Join(roots.State.Path, codexAuxiliaryNames[i]) {
			return empty, errCodexStateFile
		}
	}
	remainingBefore := *control.Read.Remaining
	parents := map[string]codexStateDirectory{}
	mergeParents := func(nodes []codexStateDirectory) error {
		for _, node := range nodes {
			if previous, ok := parents[node.Path]; ok && previous != node {
				return errCodexStateFile
			}
			parents[node.Path] = node
		}
		return nil
	}
	var entries []codexAuxiliaryEntry
	var directories []codexAuxiliaryDirectory
	var rootEntries []codexAuxiliaryEntry
	var walk func(int, string, codexStateFDChain, int) error
	walk = func(rootIndex int, relative string, chain codexStateFDChain, depth int) error {
		if ctx.Err() != nil || depth > codexAuxiliaryDepthLimit || mergeParents(chain.parents()) != nil {
			return errCodexStateFile
		}
		node := chain[len(chain)-1]
		names, err := codexStateDirectoryNames(node, codexAuxiliaryEntryLimit-len(entries))
		if err != nil {
			return err
		}
		directories = append(directories, codexAuxiliaryDirectory{root: rootIndex, relative: relative, children: names})
		for _, name := range names {
			path := filepath.Join(relative, name)
			if ctx.Err() != nil || depth+1 > codexAuxiliaryDepthLimit || len(entries) >= codexAuxiliaryEntryLimit || len(path) > 4096 || !utf8.ValidString(name) || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
				return errCodexStateFile
			}
			var stat unix.Stat_t
			if unix.Fstatat(node.fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW) != nil {
				return errCodexStateFile
			}
			entry := codexAuxiliaryEntry{Name: codexAuxiliaryNames[rootIndex] + "/" + path}
			switch uint32(stat.Mode) & unix.S_IFMT {
			case unix.S_IFREG:
				observation, err := codexReadStateFile(ctx, roots.Runtime[rootIndex], path, control.Read)
				if err != nil || !observation.File.Present || observation.File.Identity != codexFileFromStat(stat) || mergeParents(observation.Parents) != nil {
					return errCodexStateFile
				}
				entry.Kind, entry.File = "file", observation.File
				entries = append(entries, entry)
			case unix.S_IFDIR:
				child := append(codexStateFDChain(nil), chain...)
				if codexAppendStateDirectory(ctx, &child, name, true) != nil {
					return errCodexStateFile
				}
				last := child[len(child)-1]
				if last.identity != codexDirectoryFromStat(filepath.Join(node.identity.Path, name), stat) {
					_ = unix.Close(last.fd)
					return errCodexStateFile
				}
				entry.Kind, entry.Directory = "directory", last.identity
				entries = append(entries, entry)
				err = func() error { defer unix.Close(last.fd); return walk(rootIndex, path, child, depth+1) }()
				if err != nil {
					return err
				}
			default:
				return errCodexStateFile
			}
		}
		after, err := codexStateDirectoryNames(node, len(names))
		if err != nil || !reflect.DeepEqual(names, after) || chain.verify(ctx) != nil {
			return errCodexStateFile
		}
		return nil
	}
	for i, root := range roots.Runtime {
		chain, err := codexOpenAnchoredStateRoot(ctx, root)
		if err != nil {
			return empty, err
		}
		rootEntries = append(rootEntries, codexAuxiliaryEntry{Name: codexAuxiliaryNames[i], Kind: "root", Directory: chain[len(chain)-1].identity})
		err = func() error { defer chain.close(); return walk(i, "", chain, 0) }()
		if err != nil {
			return empty, err
		}
	}
	if control.BeforeGroupVerify != nil {
		control.BeforeGroupVerify()
	}
	observation := codexAuxiliaryObservation{Roots: roots, Entries: entries, Directories: directories, Parents: parents}
	if codexVerifyAuxiliaryObservation(ctx, observation) != nil {
		return empty, errCodexStateFile
	}
	if ctx.Err() != nil {
		return empty, errCodexStateFile
	}
	entries = append(rootEntries, entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	body, err := json.Marshal(entries)
	if err != nil {
		return empty, errCodexStateFile
	}
	hasher := sha256.New()
	_, _ = hasher.Write([]byte("agent-bird/codex-owned-aux-v1\x00"))
	_, _ = hasher.Write(body)
	if ctx.Err() != nil || *control.Read.Remaining < 0 || *control.Read.Remaining > remainingBefore {
		return empty, errCodexStateFile
	}
	if retained != nil {
		*retained = observation
	}
	return codexAuxiliaryState{Version: 1, SHA256: hex.EncodeToString(hasher.Sum(nil)), Entries: len(entries) - len(rootEntries), Bytes: remainingBefore - *control.Read.Remaining}, nil
}

// Reusable final, metadata-only barrier while all byte observations remain
// in memory. It never rereads content or establishes a fresh byte budget.
func codexVerifyAuxiliaryObservation(ctx context.Context, observation codexAuxiliaryObservation) error {
	if ctx.Err() != nil || len(observation.Entries) > codexAuxiliaryEntryLimit || len(observation.Directories) > codexAuxiliaryEntryLimit+5 {
		return errCodexStateFile
	}
	for _, directory := range observation.Directories {
		chain, err := codexOpenAnchoredStateRoot(ctx, observation.Roots.Runtime[directory.root])
		if err != nil {
			return err
		}
		if directory.relative != "" {
			for _, name := range strings.Split(directory.relative, string(filepath.Separator)) {
				if codexAppendStateDirectory(ctx, &chain, name, true) != nil {
					chain.close()
					return errCodexStateFile
				}
			}
		}
		for _, node := range chain {
			if expected, ok := observation.Parents[node.identity.Path]; !ok || expected != node.identity {
				chain.close()
				return errCodexStateFile
			}
		}
		names, err := codexStateDirectoryNames(chain[len(chain)-1], len(directory.children))
		checkErr := chain.verify(ctx)
		chain.close()
		if err != nil || checkErr != nil || !reflect.DeepEqual(names, directory.children) {
			return errCodexStateFile
		}
	}
	for _, entry := range observation.Entries {
		if entry.Kind != "file" {
			continue
		}
		index := -1
		for i, name := range codexAuxiliaryNames {
			if strings.HasPrefix(entry.Name, name+"/") {
				index = i
				break
			}
		}
		if index < 0 {
			return errCodexStateFile
		}
		relative := strings.TrimPrefix(entry.Name, codexAuxiliaryNames[index]+"/")
		if codexCheckPersistentSlotMetadata(ctx, observation.Roots.Runtime[index], relative, entry.File, observation.Parents) != nil {
			return errCodexStateFile
		}
	}
	if ctx.Err() != nil {
		return errCodexStateFile
	}
	return nil
}

func codexValidateAuxiliaryState(ctx context.Context, roots codexAuxiliaryRoots, previous codexAuxiliaryState, control codexAuxiliaryControl) error {
	if previous.Version != 1 || previous.Entries < 0 || previous.Entries > codexAuxiliaryEntryLimit || previous.Bytes < 0 || previous.Bytes > codexStateTotalLimit || len(previous.SHA256) != 64 {
		return errCodexStateFile
	}
	current, err := codexCaptureAuxiliaryState(ctx, roots, control)
	if err != nil || current != previous {
		return errCodexStateFile
	}
	return nil
}
