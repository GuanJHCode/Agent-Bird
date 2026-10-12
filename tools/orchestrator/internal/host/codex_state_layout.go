package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
	"golang.org/x/sys/unix"
)

const codexPrimaryEntryLimit = 64

type codexStateLayout struct {
	State     codexStateRoot
	Home      codexStateRoot
	SQLite    codexStateRoot
	Sessions  codexStateRoot
	Auxiliary codexAuxiliaryRoots
}

type codexPrimaryInventory struct {
	Parents []codexStateDirectory `json:"parents"`
	Entries int                   `json:"entries"`
	SHA256  string                `json:"sha256"`
}

type codexPrimaryControl struct {
	BeforeGroupVerify func()
}

func codexAnchorStateLayout(ctx context.Context, path string) (codexStateLayout, error) {
	var layout codexStateLayout
	var err error
	layout.State, err = codexAnchorStateRoot(ctx, path)
	if err != nil {
		return layout, err
	}
	for _, pair := range []struct {
		name string
		out  *codexStateRoot
	}{{"home", &layout.Home}, {"sqlite", &layout.SQLite}, {"home/sessions", &layout.Sessions}} {
		*pair.out, err = codexAnchorStateRoot(ctx, filepath.Join(path, pair.name))
		if err != nil {
			return codexStateLayout{}, err
		}
	}
	layout.Auxiliary.State, layout.Auxiliary.Home = layout.State, layout.Home
	for i, name := range codexAuxiliaryNames {
		layout.Auxiliary.Runtime[i], err = codexAnchorStateRoot(ctx, filepath.Join(path, name))
		if err != nil {
			return codexStateLayout{}, err
		}
	}
	return layout, nil
}

type codexPrimaryEntry struct {
	Name      string
	Kind      string
	Directory codexStateDirectory
	File      codexStateFileIdentity
	Target    string
}

type codexPrimaryScan struct {
	root     codexStateRoot
	relative string
	allowed  map[string]string
}

// A nil binding denotes the fresh pre-Start layout: no SQLite/session state
// may already exist. A bound layout admits only that exact ordinary rollout.
func codexCapturePrimaryInventory(ctx context.Context, layout codexStateLayout, binding *codexsession.ThreadBinding, control codexPrimaryControl) (codexPrimaryInventory, error) {
	var empty codexPrimaryInventory
	if ctx.Err() != nil || layout.Home.Path != filepath.Join(layout.State.Path, "home") || layout.SQLite.Path != filepath.Join(layout.State.Path, "sqlite") || layout.Sessions.Path != filepath.Join(layout.Home.Path, "sessions") || !reflect.DeepEqual(layout.State, layout.Auxiliary.State) || !reflect.DeepEqual(layout.Home, layout.Auxiliary.Home) {
		return empty, errCodexStateFile
	}
	roots := []codexStateRoot{layout.State, layout.Home, layout.SQLite, layout.Sessions}
	for i, root := range layout.Auxiliary.Runtime {
		if root.Path != filepath.Join(layout.State.Path, codexAuxiliaryNames[i]) {
			return empty, errCodexStateFile
		}
		roots = append(roots, root)
	}
	parents := map[string]codexStateDirectory{}
	mergeParents := func(chain codexStateFDChain) error {
		for _, node := range chain {
			if previous, ok := parents[node.identity.Path]; ok && previous != node.identity {
				return errCodexStateFile
			}
			parents[node.identity.Path] = node.identity
			if len(parents) > 32 {
				return errCodexStateFile
			}
		}
		return nil
	}
	for _, root := range roots {
		chain, err := codexOpenAnchoredStateRoot(ctx, root)
		if err != nil {
			return empty, err
		}
		err = mergeParents(chain)
		chain.close()
		if err != nil {
			return empty, err
		}
	}
	sqliteNames := map[string]string{}
	if binding != nil {
		for _, family := range codexSQLiteFamilies {
			for _, suffix := range []string{"", "-wal", "-shm"} {
				sqliteNames[family+".sqlite"+suffix] = "file"
			}
		}
	}
	scans := []codexPrimaryScan{
		{root: layout.State, allowed: map[string]string{"home": "directory", "sqlite": "directory", "log": "directory", "tmp": "directory"}},
		{root: layout.Home, allowed: map[string]string{"sessions": "directory", "skills": "directory", "tmp": "directory", "thread-writer-locks": "directory", "config.toml": "config", "auth.json": "auth-reference", "installation_id": "file"}},
		{root: layout.SQLite, allowed: sqliteNames},
	}
	if binding == nil {
		scans = append(scans, codexPrimaryScan{root: layout.Sessions, allowed: map[string]string{}})
	} else {
		relative, err := codexManagedRolloutRelative(layout.Home.Path, *binding)
		if err != nil {
			return empty, errCodexStateFile
		}
		parts := strings.Split(relative, string(filepath.Separator))
		for i, name := range parts {
			kind := "directory"
			if i == len(parts)-1 {
				kind = "file"
			}
			dir := ""
			if i > 0 {
				dir = filepath.Join(parts[:i]...)
			}
			scans = append(scans, codexPrimaryScan{root: layout.Sessions, relative: dir, allowed: map[string]string{name: kind}})
		}
	}
	collect := func() ([]codexPrimaryEntry, error) {
		var result []codexPrimaryEntry
		for _, scan := range scans {
			chain, err := codexOpenAnchoredStateRoot(ctx, scan.root)
			if err != nil {
				return nil, err
			}
			if scan.relative != "" {
				for _, name := range strings.Split(scan.relative, string(filepath.Separator)) {
					if codexAppendStateDirectory(ctx, &chain, name, true) != nil {
						chain.close()
						return nil, errCodexStateFile
					}
				}
			}
			if mergeParents(chain) != nil {
				chain.close()
				return nil, errCodexStateFile
			}
			node := chain[len(chain)-1]
			names, err := codexStateDirectoryNames(node, codexPrimaryEntryLimit-len(result))
			if err != nil {
				chain.close()
				return nil, err
			}
			for _, name := range names {
				kind, ok := scan.allowed[name]
				if !ok || ctx.Err() != nil {
					chain.close()
					return nil, errCodexStateFile
				}
				entry := codexPrimaryEntry{Name: filepath.Join(node.identity.Path, name), Kind: kind}
				var stat unix.Stat_t
				if unix.Fstatat(node.fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW) != nil {
					chain.close()
					return nil, errCodexStateFile
				}
				switch kind {
				case "directory":
					if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || uint32(stat.Mode)&07022 != 0 {
						chain.close()
						return nil, errCodexStateFile
					}
					entry.Directory = codexDirectoryFromStat(entry.Name, stat)
					if previous, ok := parents[entry.Name]; ok && previous != entry.Directory {
						chain.close()
						return nil, errCodexStateFile
					}
					parents[entry.Name] = entry.Directory
					if len(parents) > 32 {
						chain.close()
						return nil, errCodexStateFile
					}
				case "auth-reference":
					if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFLNK || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
						chain.close()
						return nil, errCodexStateFile
					}
					buffer := make([]byte, 4097)
					n, err := unix.Readlinkat(node.fd, name, buffer)
					if err != nil || n > 4096 || !codexCanonicalPath(string(buffer[:n])) {
						chain.close()
						return nil, errCodexStateFile
					}
					entry.Target, entry.File = string(buffer[:n]), codexFileFromStat(stat)
				case "file", "config":
					if !codexSafeOwnedFile(stat) || kind == "config" && uint32(stat.Mode)&07777 != 0600 {
						chain.close()
						return nil, errCodexStateFile
					}
					entry.File = codexFileFromStat(stat)
				default:
					chain.close()
					return nil, errCodexStateFile
				}
				result = append(result, entry)
			}
			if chain.verify(ctx) != nil {
				chain.close()
				return nil, errCodexStateFile
			}
			chain.close()
		}
		sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
		return result, nil
	}
	before, err := collect()
	if err != nil {
		return empty, err
	}
	if control.BeforeGroupVerify != nil {
		control.BeforeGroupVerify()
	}
	after, err := collect()
	if err != nil || !reflect.DeepEqual(before, after) || ctx.Err() != nil {
		return empty, errCodexStateFile
	}
	allParents := make([]codexStateDirectory, 0, len(parents))
	for _, parent := range parents {
		allParents = append(allParents, parent)
	}
	sort.Slice(allParents, func(i, j int) bool { return allParents[i].Path < allParents[j].Path })
	body, err := json.Marshal(before)
	if err != nil {
		return empty, errCodexStateFile
	}
	hasher := sha256.New()
	_, _ = hasher.Write([]byte("agent-bird/codex-owned-primary-v1\x00"))
	_, _ = hasher.Write(body)
	if ctx.Err() != nil {
		return empty, errCodexStateFile
	}
	return codexPrimaryInventory{Parents: allParents, Entries: len(before), SHA256: hex.EncodeToString(hasher.Sum(nil))}, nil
}
