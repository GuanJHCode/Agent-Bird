package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

var errCodexStateFile = errors.New("codex_state_file_untrusted")

const codexStateFileLimit int64 = 64 * 1024 * 1024
const codexStateTotalLimit int64 = 128 * 1024 * 1024

type codexStateDirectory struct {
	Path  string `json:"path"`
	Dev   uint64 `json:"dev"`
	Inode uint64 `json:"inode"`
	UID   uint32 `json:"uid"`
	Mode  uint32 `json:"mode"`
}

type codexStateFileIdentity struct {
	Dev       uint64 `json:"dev"`
	Inode     uint64 `json:"inode"`
	UID       uint32 `json:"uid"`
	Mode      uint32 `json:"mode"`
	Nlink     uint64 `json:"nlink"`
	Size      int64  `json:"size"`
	MtimeSec  int64  `json:"mtime_sec"`
	MtimeNsec int64  `json:"mtime_nsec"`
	CtimeSec  int64  `json:"ctime_sec"`
	CtimeNsec int64  `json:"ctime_nsec"`
}

type codexStateFile struct {
	Present  bool                   `json:"present"`
	Identity codexStateFileIdentity `json:"identity"`
	SHA256   string                 `json:"sha256"`
}

// Head/Tail are private format observations, never serialized into a seal.
// This helper is only for owned runtime/config bytes, never source auth bytes.
type codexStateObservation struct {
	File      codexStateFile        `json:"file"`
	Head      [100]byte             `json:"-"`
	HeadBytes int                   `json:"-"`
	Tail      byte                  `json:"-"`
	Parents   []codexStateDirectory `json:"-"`
}

type codexStateRoot struct {
	Path    string
	Parents []codexStateDirectory
}

type codexStateReadControl struct {
	FileLimit    int64
	Remaining    *int64
	BeforeVerify func(int)
}

type codexStateFDNode struct {
	fd       int
	name     string
	identity codexStateDirectory
}

type codexStateFDChain []codexStateFDNode

func (chain codexStateFDChain) parents() []codexStateDirectory {
	result := make([]codexStateDirectory, len(chain))
	for i, node := range chain {
		result[i] = node.identity
	}
	return result
}

func (chain codexStateFDChain) close() {
	for i := len(chain) - 1; i >= 0; i-- {
		_ = unix.Close(chain[i].fd)
	}
}

func codexDirectoryFromStat(path string, stat unix.Stat_t) codexStateDirectory {
	return codexStateDirectory{Path: path, Dev: uint64(stat.Dev), Inode: stat.Ino, UID: stat.Uid, Mode: uint32(stat.Mode) & 07777}
}

func codexFileFromStat(stat unix.Stat_t) codexStateFileIdentity {
	return codexStateFileIdentity{Dev: uint64(stat.Dev), Inode: stat.Ino, UID: stat.Uid, Mode: uint32(stat.Mode) & 07777, Nlink: uint64(stat.Nlink), Size: stat.Size, MtimeSec: stat.Mtim.Sec, MtimeNsec: stat.Mtim.Nsec, CtimeSec: stat.Ctim.Sec, CtimeNsec: stat.Ctim.Nsec}
}

func codexSafeOwnedFile(stat unix.Stat_t) bool {
	return uint32(stat.Mode)&unix.S_IFMT == unix.S_IFREG && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1 && uint32(stat.Mode)&07022 == 0 && stat.Size >= 0
}

// Keep the original parent FDs until the complete read has been verified.
// Directory metadata that native direntry changes legitimately mutate is not
// identity; ownership/mode/dev/inode and each original parent->child edge are.
func (chain codexStateFDChain) verify(ctx context.Context) error {
	if ctx.Err() != nil {
		return errCodexStateFile
	}
	for i, node := range chain {
		var stat unix.Stat_t
		if unix.Fstat(node.fd, &stat) != nil || uint32(stat.Mode)&unix.S_IFMT != unix.S_IFDIR || codexDirectoryFromStat(node.identity.Path, stat) != node.identity {
			return errCodexStateFile
		}
		if i > 0 {
			if unix.Fstatat(chain[i-1].fd, node.name, &stat, unix.AT_SYMLINK_NOFOLLOW) != nil || uint32(stat.Mode)&unix.S_IFMT != unix.S_IFDIR || codexDirectoryFromStat(node.identity.Path, stat) != node.identity {
				return errCodexStateFile
			}
		}
	}
	if ctx.Err() != nil {
		return errCodexStateFile
	}
	return nil
}

func codexAppendStateDirectory(ctx context.Context, chain *codexStateFDChain, name string, owned bool) error {
	if ctx.Err() != nil || len(*chain) == 0 || len(*chain) >= 64 {
		return errCodexStateFile
	}
	parent := (*chain)[len(*chain)-1]
	fd, err := unix.Openat(parent.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return errCodexStateFile
	}
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || uint32(stat.Mode)&unix.S_IFMT != unix.S_IFDIR || (owned && (stat.Uid != uint32(os.Geteuid()) || uint32(stat.Mode)&07022 != 0)) {
		_ = unix.Close(fd)
		return errCodexStateFile
	}
	*chain = append(*chain, codexStateFDNode{fd: fd, name: name, identity: codexDirectoryFromStat(filepath.Join(parent.identity.Path, name), stat)})
	return nil
}

func codexOpenStateRoot(ctx context.Context, path string) (codexStateFDChain, error) {
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
		if err = codexAppendStateDirectory(ctx, &chain, name, false); err != nil {
			chain.close()
			return nil, err
		}
	}
	last := chain[len(chain)-1].identity
	if last.UID != uint32(os.Geteuid()) || last.Mode != 0700 || chain.verify(ctx) != nil {
		chain.close()
		return nil, errCodexStateFile
	}
	return chain, nil
}

func codexAnchorStateRoot(ctx context.Context, path string) (codexStateRoot, error) {
	chain, err := codexOpenStateRoot(ctx, path)
	if err != nil {
		return codexStateRoot{}, err
	}
	defer chain.close()
	root := codexStateRoot{Path: path, Parents: make([]codexStateDirectory, len(chain))}
	for i, node := range chain {
		root.Parents[i] = node.identity
	}
	return root, nil
}

// All bytes are read from the matched original regular FD. The initial size
// is a precheck, not permission to truncate: the same-FD size/times and path
// identity must still be equal after reading exactly that many actual bytes.
func codexReadStateFile(ctx context.Context, root codexStateRoot, relative string, control codexStateReadControl) (codexStateObservation, error) {
	var observation codexStateObservation
	limit := control.FileLimit
	if limit == 0 {
		limit = codexStateFileLimit
	}
	if limit < 1 || limit > codexStateFileLimit || control.Remaining == nil || *control.Remaining < 0 || *control.Remaining > codexStateTotalLimit || ctx.Err() != nil || relative == "" || len(relative) > 4096 || !utf8.ValidString(relative) || strings.ContainsRune(relative, 0) || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relative == "." {
		return observation, errCodexStateFile
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) > 32 {
		return observation, errCodexStateFile
	}
	for _, name := range parts {
		if name == "." || name == ".." || name == "" {
			return observation, errCodexStateFile
		}
	}
	chain, err := codexOpenStateRoot(ctx, root.Path)
	if err != nil {
		return observation, err
	}
	defer func() { chain.close() }()
	if len(chain) != len(root.Parents) {
		return observation, errCodexStateFile
	}
	for i, node := range chain {
		if node.identity != root.Parents[i] {
			return observation, errCodexStateFile
		}
	}
	for _, name := range parts[:len(parts)-1] {
		if codexAppendStateDirectory(ctx, &chain, name, true) != nil {
			return observation, errCodexStateFile
		}
	}
	parent, name := chain[len(chain)-1].fd, parts[len(parts)-1]
	var initial unix.Stat_t
	err = unix.Fstatat(parent, name, &initial, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		if control.BeforeVerify != nil {
			control.BeforeVerify(-1)
		}
		if !errors.Is(unix.Fstatat(parent, name, &initial, unix.AT_SYMLINK_NOFOLLOW), unix.ENOENT) || chain.verify(ctx) != nil {
			return observation, errCodexStateFile
		}
		observation.Parents = chain.parents()
		return observation, nil
	}
	if err != nil || !codexSafeOwnedFile(initial) || initial.Size > limit || initial.Size > *control.Remaining {
		return observation, errCodexStateFile
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return observation, errCodexStateFile
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var before unix.Stat_t
	if unix.Fstat(fd, &before) != nil || !codexSafeOwnedFile(before) || codexFileFromStat(before) != codexFileFromStat(initial) {
		return observation, errCodexStateFile
	}
	hasher := sha256.New()
	buffer := make([]byte, 32*1024)
	left := before.Size
	for left > 0 {
		if ctx.Err() != nil {
			return codexStateObservation{}, errCodexStateFile
		}
		next := int64(len(buffer))
		if left < next {
			next = left
		}
		n, readErr := f.Read(buffer[:int(next)])
		if n > 0 {
			left -= int64(n)
			*control.Remaining -= int64(n)
			_, _ = hasher.Write(buffer[:n])
			observation.HeadBytes += copy(observation.Head[observation.HeadBytes:], buffer[:n])
			observation.Tail = buffer[n-1]
		}
		if readErr != nil && !(errors.Is(readErr, io.EOF) && left == 0) || n == 0 {
			return codexStateObservation{}, errCodexStateFile
		}
	}
	if control.BeforeVerify != nil {
		control.BeforeVerify(fd)
	}
	var after, current unix.Stat_t
	if unix.Fstat(fd, &after) != nil || codexFileFromStat(after) != codexFileFromStat(before) || unix.Fstatat(parent, name, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || !codexSafeOwnedFile(current) || codexFileFromStat(current) != codexFileFromStat(before) || chain.verify(ctx) != nil {
		return codexStateObservation{}, errCodexStateFile
	}
	observation.File = codexStateFile{Present: true, Identity: codexFileFromStat(before), SHA256: hex.EncodeToString(hasher.Sum(nil))}
	observation.Parents = chain.parents()
	return observation, nil
}
