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
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexconfig"
	"golang.org/x/sys/unix"
)

type codexSourceReferences struct {
	Home    string                `json:"home"`
	Parents []codexStateDirectory `json:"parents"`
	Config  codexStateFile        `json:"config"`
	// Auth is metadata only. No auth bytes or content digest are ever obtained.
	Auth            codexStateFileIdentity `json:"auth"`
	ProjectedSHA256 string                 `json:"projected_sha256"`
	OverridesSHA256 string                 `json:"overrides_sha256"`
}

type codexSourceControl struct{ BeforeVerify func(string, int) }

// The source root is recorded as it exists, never chmodded. Safe original
// 0700/0755 roots are admitted; owned runtime/protected roots still require0700.
func codexOpenSourceRoot(ctx context.Context, path string) (codexStateFDChain, error) {
	if ctx.Err() != nil || !codexCanonicalPath(path) || path == string(filepath.Separator) {
		return nil, errCodexStateFile
	}
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	if len(parts)+1 > 32 {
		return nil, errCodexStateFile
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errCodexStateFile
	}
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil {
		_ = unix.Close(fd)
		return nil, errCodexStateFile
	}
	chain := codexStateFDChain{{fd: fd, identity: codexDirectoryFromStat(string(filepath.Separator), stat)}}
	for _, name := range parts {
		if codexAppendStateDirectory(ctx, &chain, name, false) != nil {
			chain.close()
			return nil, errCodexStateFile
		}
	}
	last := chain[len(chain)-1].identity
	if last.UID != uint32(os.Geteuid()) || last.Mode&07022 != 0 || chain.verify(ctx) != nil {
		chain.close()
		return nil, errCodexStateFile
	}
	return chain, nil
}

// Only the two literal source names are permitted. Config is read once through
// the matched original FD; auth is opened/Fstatted and immediately closed with
// no Read, hashing, copying or export of its contents.
func codexObserveSourceFile(ctx context.Context, chain codexStateFDChain, name string, read bool, control codexSourceControl) (codexStateFile, []byte, error) {
	var file codexStateFile
	if ctx.Err() != nil || (name != "config.toml" && name != "auth.json") || read != (name == "config.toml") || len(chain) == 0 {
		return file, nil, errCodexStateFile
	}
	parent := chain[len(chain)-1].fd
	var initial unix.Stat_t
	err := unix.Fstatat(parent, name, &initial, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) && read {
		if control.BeforeVerify != nil {
			control.BeforeVerify(name, -1)
		}
		if !errors.Is(unix.Fstatat(parent, name, &initial, unix.AT_SYMLINK_NOFOLLOW), unix.ENOENT) || chain.verify(ctx) != nil {
			return file, nil, errCodexStateFile
		}
		return file, nil, nil
	}
	if err != nil || !codexSafeOwnedFile(initial) || initial.Size > codexStateFileLimit || (read && initial.Size > codexconfig.MaxInputBytes) {
		return file, nil, errCodexStateFile
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return file, nil, errCodexStateFile
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var opened unix.Stat_t
	if unix.Fstat(fd, &opened) != nil || !codexSafeOwnedFile(opened) || codexFileFromStat(opened) != codexFileFromStat(initial) {
		return file, nil, errCodexStateFile
	}
	var body []byte
	if read {
		body = make([]byte, int(initial.Size))
		for offset := 0; offset < len(body); {
			if ctx.Err() != nil {
				return file, nil, errCodexStateFile
			}
			end := offset + 32768
			if end > len(body) {
				end = len(body)
			}
			n, err := io.ReadFull(f, body[offset:end])
			offset += n
			if err != nil {
				return file, nil, errCodexStateFile
			}
		}
	}
	if control.BeforeVerify != nil {
		control.BeforeVerify(name, fd)
	}
	var after, current unix.Stat_t
	if unix.Fstat(fd, &after) != nil || codexFileFromStat(after) != codexFileFromStat(initial) || unix.Fstatat(parent, name, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || !codexSafeOwnedFile(current) || codexFileFromStat(current) != codexFileFromStat(initial) || chain.verify(ctx) != nil {
		return file, nil, errCodexStateFile
	}
	file = codexStateFile{Present: true, Identity: codexFileFromStat(initial)}
	if read {
		sum := sha256.Sum256(body)
		file.SHA256 = hex.EncodeToString(sum[:])
	}
	return file, body, nil
}

func codexCaptureSourceReferences(ctx context.Context, home string, control codexSourceControl) (codexSourceReferences, *codexconfig.Projection, error) {
	var refs codexSourceReferences
	chain, err := codexOpenSourceRoot(ctx, home)
	if err != nil {
		return refs, nil, err
	}
	defer chain.close()
	config, body, err := codexObserveSourceFile(ctx, chain, "config.toml", true, control)
	if err != nil {
		return refs, nil, err
	}
	projection, err := codexconfig.Project(body, codexconfig.ReadOnly)
	if err != nil {
		return refs, nil, err
	}
	auth, _, err := codexObserveSourceFile(ctx, chain, "auth.json", false, control)
	if err != nil {
		return refs, nil, err
	}
	overrides, err := json.Marshal(projection.ManagedOverrides())
	if err != nil {
		return refs, nil, errCodexStateFile
	}
	refs = codexSourceReferences{Home: home, Parents: chain.parents(), Config: config, Auth: auth.Identity, ProjectedSHA256: codexDigest(projection.ConfigTOML()), OverridesSHA256: codexDigest(overrides)}
	if codexVerifySourceReferences(ctx, refs) != nil {
		return codexSourceReferences{}, nil, errCodexStateFile
	}
	return refs, projection, nil
}

func codexDigest(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

// This final reference barrier checks identities/absence without another read
// or another config projection. It is joined again immediately before a seal.
func codexVerifySourceReferences(ctx context.Context, refs codexSourceReferences) error {
	chain, err := codexOpenSourceRoot(ctx, refs.Home)
	if err != nil {
		return err
	}
	defer chain.close()
	if !reflect.DeepEqual(chain.parents(), refs.Parents) {
		return errCodexStateFile
	}
	for _, name := range []string{"config.toml", "auth.json"} {
		var stat unix.Stat_t
		err = unix.Fstatat(chain[len(chain)-1].fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
		present, identity := refs.Config.Present, refs.Config.Identity
		if name == "auth.json" {
			present = true
			identity = refs.Auth
		}
		if !present {
			if !errors.Is(err, unix.ENOENT) {
				return errCodexStateFile
			}
		} else if err != nil || !codexSafeOwnedFile(stat) || codexFileFromStat(stat) != identity {
			return errCodexStateFile
		}
	}
	return chain.verify(ctx)
}
