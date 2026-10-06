package host

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// The hidden app_data_dir option is verified only for this exact native binary.
// A claimed version from a different binary does not authorize that option.
const agyHistoryVersion = "1.2.16"
const agyHistorySHA256 = "7dca095cfc1df2c057a385ed88a76c7ba98dc103258a80be87a8f42e484cb3aa"
const agyConfigLimit = 1024 * 1024

type agyConfigReference struct {
	Name         string                   `json:"name"`
	Missing      bool                     `json:"missing"`
	Identity     continuationFileIdentity `json:"identity"`
	Size         int64                    `json:"size"`
	SHA256       string                   `json:"sha256"`
	LinkIdentity continuationFileIdentity `json:"link_identity"`
}

type agyHistoryBinding struct {
	Version               int                      `json:"version"`
	Origin                continuationOrigin       `json:"origin"`
	Pin                   adapter.BinaryPin        `json:"pin"`
	Home                  string                   `json:"home"`
	HomeIdentity          continuationFileIdentity `json:"home_identity"`
	Gemini                string                   `json:"gemini"`
	GeminiIdentity        continuationFileIdentity `json:"gemini_identity"`
	OriginalAppMissing    bool                     `json:"original_app_missing"`
	OriginalAppIdentity   continuationFileIdentity `json:"original_app_identity"`
	OriginalCacheMissing  bool                     `json:"original_cache_missing"`
	OriginalCacheIdentity continuationFileIdentity `json:"original_cache_identity"`
	Workspace             string                   `json:"workspace"`
	WorkspaceIdentity     continuationFileIdentity `json:"workspace_identity"`
	Relative              string                   `json:"relative"`
	Path                  string                   `json:"path"`
	Identity              continuationFileIdentity `json:"identity"`
	CacheIdentity         continuationFileIdentity `json:"cache_identity"`
	References            [3]agyConfigReference    `json:"references"`
}

type agyHistoryScope struct {
	Path      string
	Protected []string
}

func agyOptionalDirectoryIdentity(path string) (continuationFileIdentity, bool, error) {
	identity, err := continuationDirectoryIdentity(path, 0)
	if errors.Is(err, os.ErrNotExist) {
		return continuationFileIdentity{}, true, nil
	}
	return identity, false, err
}

// Read only configuration, never the separate native account/token files.
// Identity and digest come from the same bounded NOFOLLOW fd. No file contents
// are copied into history metadata or diagnostics.
func agyFileSnapshot(path, name string, limit int64) (agyConfigReference, error) {
	return agyFileSnapshotChecked(path, name, limit, false)
}

// SQLite's header is checked on the exact bytes used for this fd's digest.
// This recognizes the file format; it does not open or validate a database.
func agyFileSnapshotChecked(path, name string, limit int64, sqlite bool) (agyConfigReference, error) {
	ref := agyConfigReference{Name: name}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		ref.Missing = true
		return ref, nil
	}
	if err != nil {
		return ref, errors.New("agy_config_source_untrusted")
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return ref, err
	}
	owner, ok := before.Sys().(*syscall.Stat_t)
	canonical, canonicalErr := filepath.EvalSymlinks(path)
	if canonicalErr != nil || canonical != path || !before.Mode().IsRegular() || before.Mode().Perm()&0022 != 0 || before.Size() > limit || !ok || owner.Uid != uint32(os.Geteuid()) || owner.Nlink != 1 {
		return ref, errors.New("agy_config_source_untrusted")
	}
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(body)) > limit {
		return ref, errors.New("agy_config_source_untrusted")
	}
	if sqlite && (len(body) < 100 || !bytes.HasPrefix(body, []byte("SQLite format 3\x00"))) {
		return ref, errors.New("agy_history_sqlite_header_invalid")
	}
	after, err := f.Stat()
	current, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !os.SameFile(before, after) || !os.SameFile(after, current) || before.Size() != after.Size() || int64(len(body)) != after.Size() || !before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() {
		return ref, errors.New("agy_config_source_changed")
	}
	ref.Identity = continuationFileIdentity{uint64(owner.Dev), owner.Ino, owner.Uid, uint32(before.Mode().Perm())}
	ref.Size, ref.SHA256 = int64(len(body)), hashBytes(body)
	return ref, nil
}

func agyLinkIdentity(path, target string) (continuationFileIdentity, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return continuationFileIdentity{}, err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	actual, err := os.Readlink(path)
	if err != nil || st.Mode()&os.ModeSymlink == 0 || !ok || owner.Uid != uint32(os.Geteuid()) || owner.Nlink != 1 || actual != target {
		return continuationFileIdentity{}, errors.New("agy_config_reference_changed")
	}
	return continuationFileIdentity{uint64(owner.Dev), owner.Ino, owner.Uid, uint32(st.Mode().Perm())}, nil
}

func (h *Host) prepareAGYHistory(cmd process.Command, g contract.LaunchCommand, pin adapter.BinaryPin) (process.Command, agyHistoryScope, error) {
	scope := agyHistoryScope{}
	if pin.Version != agyHistoryVersion || pin.SHA256 != agyHistorySHA256 {
		if g.Continuation != nil {
			return cmd, scope, errors.New("agy_session_history_not_verified")
		}
		return cmd, scope, nil
	}
	if pin.Path != cmd.Path || h.continuationOwner == "" {
		return cmd, scope, errors.New("agy_history_origin_untrusted")
	}
	for _, arg := range cmd.Args {
		if arg == "--app_data_dir" || strings.HasPrefix(arg, "--app_data_dir=") || arg == "--gemini_dir" || strings.HasPrefix(arg, "--gemini_dir=") {
			return cmd, scope, errors.New("agy_history_path_override")
		}
	}
	home := commandEnvironment(cmd, "HOME")
	if home == "" || !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return cmd, scope, errors.New("agy_home_untrusted")
	}
	homeID, err := continuationDirectoryIdentity(home, 0)
	if err != nil {
		return cmd, scope, err
	}
	gemini := filepath.Join(home, ".gemini")
	geminiID, err := continuationDirectoryIdentity(gemini, 0)
	if err != nil {
		return cmd, scope, err
	}
	workspaceID, err := continuationDirectoryIdentity(cmd.Dir, 0)
	if err != nil {
		return cmd, scope, err
	}
	source, err := h.originalContinuationSegment(g)
	if err != nil {
		return cmd, scope, err
	}
	sourceGrant := g
	sourceGrant.SegmentID = source
	component := "agent-bird-" + hashText(g.RunID + "\x00" + g.TaskID + "\x00" + g.AttemptID + "\x00" + strconv.Itoa(g.WorkRevision))[:48]
	owned := filepath.Join(gemini, component)
	original := filepath.Join(gemini, "antigravity-cli")
	originalID, originalMissing, err := agyOptionalDirectoryIdentity(original)
	if err != nil {
		return cmd, scope, err
	}
	cacheID, cacheMissing, err := agyOptionalDirectoryIdentity(filepath.Join(original, "cache"))
	if err != nil {
		return cmd, scope, err
	}
	want := agyHistoryBinding{Version: 1, Origin: h.originForGrant(sourceGrant, "antigravity-cli"), Pin: pin,
		Home: home, HomeIdentity: homeID, Gemini: gemini, GeminiIdentity: geminiID,
		OriginalAppIdentity: originalID, OriginalAppMissing: originalMissing,
		OriginalCacheIdentity: cacheID, OriginalCacheMissing: cacheMissing,
		Workspace: cmd.Dir, WorkspaceIdentity: workspaceID, Relative: component, Path: owned}
	for i, name := range [3]string{"settings.json", "keybindings.json", "cache/default_project_id.txt"} {
		want.References[i], err = agyFileSnapshot(filepath.Join(original, name), name, agyConfigLimit)
		if err != nil {
			return cmd, scope, err
		}
	}
	metadata := filepath.Join(h.spoolRoot, g.AttemptID, source, "provider-history.json")
	var bound agyHistoryBinding
	err = readContinuationMetadata(metadata, &bound)
	creating := errors.Is(err, os.ErrNotExist) && g.Continuation == nil
	if creating {
		if err = os.Mkdir(owned, 0700); err != nil {
			return cmd, scope, errors.New("agy_history_directory_reused")
		}
		if err = os.Mkdir(filepath.Join(owned, "cache"), 0700); err != nil {
			return cmd, scope, err
		}
		for _, ref := range want.References {
			if !ref.Missing {
				if err = os.Symlink(filepath.Join(original, ref.Name), filepath.Join(owned, ref.Name)); err != nil {
					return cmd, scope, err
				}
			}
		}
	} else if err != nil {
		return cmd, scope, err
	}
	want.Identity, err = continuationDirectoryIdentity(owned, 0700)
	if err != nil {
		return cmd, scope, err
	}
	want.CacheIdentity, err = continuationDirectoryIdentity(filepath.Join(owned, "cache"), 0700)
	if err != nil {
		return cmd, scope, err
	}
	for i, ref := range want.References {
		link, source := filepath.Join(owned, ref.Name), filepath.Join(original, ref.Name)
		scope.Protected = append(scope.Protected, source)
		if ref.Missing && ref.Name == "cache/default_project_id.txt" {
			// Only the original-source absence is pinned: native creation of its
			// owned mutable index is permitted, never original project repair.
			continue
		}
		scope.Protected = append(scope.Protected, link)
		if ref.Name == "cache/default_project_id.txt" {
			scope.Protected = append(scope.Protected, filepath.Dir(link))
		}
		if ref.Missing {
			if _, err = os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
				return cmd, scope, errors.New("agy_missing_config_changed")
			}
		} else {
			want.References[i].LinkIdentity, err = agyLinkIdentity(link, source)
			if err != nil {
				return cmd, scope, err
			}
		}
	}
	if creating {
		spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: source})
		if err != nil {
			return cmd, scope, err
		}
		if err = spool.WriteMetadata("provider-history.json", want); err != nil {
			return cmd, scope, err
		}
		bound = want
	}
	if bound != want {
		return cmd, scope, errors.New("agy_history_binding_changed")
	}
	if g.Continuation != nil {
		if err = h.validateAGYHistorySeal(g, bound); err != nil {
			return cmd, scope, err
		}
	}
	// Keep the parent's argv/env untouched. Native HOME, GeminiDir and the
	// independent config root remain at their original values.
	cmd.Args = append(append([]string(nil), cmd.Args...), "--app_data_dir", component)
	scope.Path = owned
	scope.Protected = append(scope.Protected, owned)
	return cmd, scope, nil
}
