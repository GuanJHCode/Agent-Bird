//go:build darwin || linux

package codextools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

type identity struct{ dev, ino uint64 }

type Broker struct {
	mu       sync.Mutex
	root     *os.File
	rootPath string
	rootID   identity
	options  Options
	written  int64
	unknown  bool
	closed   bool
}

func Open(root string, options Options) (*Broker, error) {
	if options.Writable && (options.MaxWrittenBytes <= 0 || options.BeforeWrite == nil || options.AfterWrite == nil) {
		return nil, errors.New("codextools_write_options_invalid")
	}
	if !options.Writable && (options.MaxWrittenBytes != 0 || options.BeforeWrite != nil || options.AfterWrite != nil) {
		return nil, errors.New("codextools_readonly_options_invalid")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) {
		return nil, errors.New("codextools_root_invalid")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return nil, errors.New("codextools_root_invalid")
	}
	before, err := os.Lstat(root)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || !owned(before) {
		return nil, errors.New("codextools_root_invalid")
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("codextools_root_invalid")
	}
	f := os.NewFile(uintptr(fd), root)
	held, err := f.Stat()
	if err != nil || !os.SameFile(before, held) || !owned(held) {
		_ = f.Close()
		return nil, errors.New("codextools_root_invalid")
	}
	id, ok := fileIdentity(held)
	if !ok {
		_ = f.Close()
		return nil, errors.New("codextools_root_invalid")
	}
	return &Broker{root: f, rootPath: root, rootID: id, options: options}, nil
}

func (b *Broker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	return b.root.Close()
}

func (b *Broker) Read(ctx context.Context, path string) ([]byte, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ready(ctx); err != nil {
		return nil, "", err
	}
	parts, err := filePath(path)
	if err != nil {
		return nil, "", err
	}
	parent, err := b.openDir(parts[:len(parts)-1])
	if err != nil {
		return nil, "", err
	}
	defer parent.Close()
	f, err := openFile(parent, parts[len(parts)-1], unix.O_RDONLY|unix.O_NONBLOCK)
	if err != nil {
		return nil, "", errors.New("codextools_file_invalid")
	}
	defer f.Close()
	if err = b.checkBinding(parts[:len(parts)-1], parent); err != nil {
		return nil, "", err
	}
	if _, err = safeRegular(f); err != nil {
		return nil, "", err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil || int64(len(data)) > maxFileBytes {
		return nil, "", errors.New("codextools_file_invalid")
	}
	if err = b.checkBinding(parts[:len(parts)-1], parent); err != nil {
		return nil, "", err
	}
	if _, err = safeRegular(f); err != nil {
		return nil, "", err
	}
	if err = sameAt(parent, parts[len(parts)-1], f); err != nil {
		return nil, "", errors.New("codextools_file_changed")
	}
	return data, digest(data), nil
}

func (b *Broker) List(ctx context.Context, path string) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ready(ctx); err != nil {
		return nil, err
	}
	parts, err := directoryPath(path)
	if err != nil {
		return nil, err
	}
	dir, err := b.openDir(parts)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	if err = b.checkBinding(parts, dir); err != nil {
		return nil, err
	}
	names, err := dir.Readdirnames(257)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(names) > 256 {
		return nil, errors.New("codextools_list_limit")
	}
	if err = b.checkBinding(parts, dir); err != nil {
		return nil, err
	}
	filtered := names[:0]
	for _, name := range names {
		if !protected(name) {
			filtered = append(filtered, name)
		}
	}
	sort.Strings(filtered)
	return filtered, nil
}

func (b *Broker) Write(ctx context.Context, path, expectedSHA256 string, content []byte) (WriteReceipt, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.unknown {
		return WriteReceipt{}, ErrUnknown
	}
	if err := b.ready(ctx); err != nil {
		return WriteReceipt{}, err
	}
	if !b.options.Writable {
		return WriteReceipt{}, ErrReadonly
	}
	parts, err := filePath(path)
	if err != nil {
		return WriteReceipt{}, err
	}
	if expectedSHA256 != "" && !validSHA(expectedSHA256) {
		return WriteReceipt{}, ErrConflict
	}
	// Options has no capability that proves a caller holds an exclusive
	// workspace write lease. A replacement would require an overwrite rename,
	// which cannot provide a compare-and-swap guarantee against another writer.
	// Keep this path fail-closed until such a lease is part of the contract.
	if expectedSHA256 != "" {
		return WriteReceipt{}, ErrConflict
	}
	if int64(len(content)) > maxFileBytes {
		return WriteReceipt{}, ErrBudget
	}
	frozen := append([]byte(nil), content...)
	if b.written > b.options.MaxWrittenBytes-int64(len(frozen)) {
		return WriteReceipt{}, ErrBudget
	}
	intent := WriteIntent{Path: path, ExpectedSHA256: expectedSHA256, ContentSHA256: digest(frozen), Bytes: int64(len(frozen))}
	if err = b.options.BeforeWrite(ctx, intent); err != nil {
		return WriteReceipt{}, err
	}
	if err = b.ready(ctx); err != nil {
		return WriteReceipt{}, err
	}
	parent, err := b.openDir(parts[:len(parts)-1])
	if err != nil {
		return WriteReceipt{}, err
	}
	defer parent.Close()
	name := parts[len(parts)-1]
	if err = requireAbsent(parent, name); err != nil {
		return WriteReceipt{}, ErrConflict
	}
	stage, stageName, stageID, err := stageFile(parent)
	if err != nil {
		return WriteReceipt{}, err
	}
	published := false
	defer func() {
		if !published {
			removeStageIfSame(parent, stageName, stageID)
		}
	}()
	if _, err = stage.Write(frozen); err == nil {
		err = stage.Sync()
	}
	if closeErr := stage.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return WriteReceipt{}, err
	}
	if err = verifyStage(parent, stageName, stageID, intent.ContentSHA256); err != nil {
		return WriteReceipt{}, err
	}
	if err = b.checkBinding(parts[:len(parts)-1], parent); err != nil {
		return WriteReceipt{}, err
	}
	if err = requireAbsent(parent, name); err != nil {
		return WriteReceipt{}, err
	}
	if err = ctx.Err(); err != nil {
		return WriteReceipt{}, err
	}
	if err = unix.Linkat(int(parent.Fd()), stageName, int(parent.Fd()), name, 0); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return WriteReceipt{}, ErrConflict
		}
		return WriteReceipt{}, err
	}
	if err = unlinkStage(parent, stageName, stageID); err != nil {
		b.unknown = true
		return WriteReceipt{}, ErrUnknown
	}
	published = true
	if err = parent.Sync(); err != nil {
		b.unknown = true
		return WriteReceipt{}, ErrUnknown
	}
	receipt := WriteReceipt{Path: path, SHA256: intent.ContentSHA256, Bytes: intent.Bytes}
	b.written += receipt.Bytes
	if err = b.options.AfterWrite(ctx, receipt); err != nil {
		b.unknown = true
		return WriteReceipt{}, fmt.Errorf("%w: %v", ErrUnknown, err)
	}
	return receipt, nil
}

func (b *Broker) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.closed {
		return errors.New("codextools_closed")
	}
	return b.checkRoot()
}

func (b *Broker) checkRoot() error {
	pathInfo, err := os.Lstat(b.rootPath)
	if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 || !owned(pathInfo) {
		return errors.New("codextools_root_changed")
	}
	held, err := b.root.Stat()
	if err != nil || !os.SameFile(pathInfo, held) {
		return errors.New("codextools_root_changed")
	}
	id, ok := fileIdentity(held)
	if !ok || id != b.rootID {
		return errors.New("codextools_root_changed")
	}
	return nil
}

func (b *Broker) openDir(parts []string) (*os.File, error) {
	if err := b.checkRoot(); err != nil {
		return nil, err
	}
	current, err := openDirectory(b.root, ".")
	if err != nil {
		return nil, err
	}
	if err = sameAt(b.root, ".", current); err != nil {
		_ = current.Close()
		return nil, errors.New("codextools_root_changed")
	}
	for _, part := range parts {
		next, err := openDirectory(current, part)
		if err != nil {
			current.Close()
			return nil, errors.New("codextools_path_invalid")
		}
		if err = sameAt(current, part, next); err != nil || !ownedDir(next) {
			next.Close()
			current.Close()
			return nil, errors.New("codextools_path_invalid")
		}
		current.Close()
		current = next
	}
	return current, nil
}

func (b *Broker) checkBinding(parts []string, held *os.File) error {
	if err := b.checkRoot(); err != nil {
		return err
	}
	current, err := b.openDir(parts)
	if err != nil {
		return err
	}
	defer current.Close()
	currentInfo, currentErr := current.Stat()
	heldInfo, heldErr := held.Stat()
	a, oka := fileIdentity(currentInfo)
	bID, okb := fileIdentity(heldInfo)
	if currentErr != nil || heldErr != nil || !oka || !okb || a != bID {
		return errors.New("codextools_path_changed")
	}
	return nil
}

func requireAbsent(parent *os.File, name string) error {
	var named unix.Stat_t
	err := unix.Fstatat(int(parent.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return ErrConflict
}

func stageFile(parent *os.File) (*os.File, string, identity, error) {
	for range 32 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", identity{}, err
		}
		name := ".codextools-" + hex.EncodeToString(random[:])
		fd, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", identity{}, err
		}
		file := os.NewFile(uintptr(fd), name)
		id, err := safeRegular(file)
		if err != nil {
			_ = file.Close()
			return nil, "", identity{}, err
		}
		return file, name, id, nil
	}
	return nil, "", identity{}, errors.New("codextools_stage_conflict")
}

func verifyStage(parent *os.File, name string, want identity, digestWant string) error {
	file, err := openFile(parent, name, unix.O_RDONLY|unix.O_NONBLOCK)
	if err != nil {
		return errors.New("codextools_stage_changed")
	}
	defer file.Close()
	id, err := safeRegular(file)
	if err != nil || id != want {
		return errors.New("codextools_stage_changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil || int64(len(data)) > maxFileBytes || digest(data) != digestWant {
		return errors.New("codextools_stage_changed")
	}
	return nil
}

func removeStageIfSame(parent *os.File, name string, want identity) {
	var named unix.Stat_t
	if unix.Fstatat(int(parent.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW) == nil && (identity{dev: uint64(named.Dev), ino: uint64(named.Ino)}) == want {
		_ = unix.Unlinkat(int(parent.Fd()), name, 0)
	}
}

func unlinkStage(parent *os.File, name string, want identity) error {
	var named unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || (identity{dev: uint64(named.Dev), ino: uint64(named.Ino)}) != want {
		return errors.New("codextools_stage_changed")
	}
	return unix.Unlinkat(int(parent.Fd()), name, 0)
}

func filePath(path string) ([]string, error)      { return splitPath(path, false) }
func directoryPath(path string) ([]string, error) { return splitPath(path, true) }

func splitPath(path string, allowEmpty bool) ([]string, error) {
	if path == "" && allowEmpty {
		return nil, nil
	}
	if path == "" || filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) || strings.Contains(path, `\\`) {
		return nil, errors.New("codextools_path_invalid")
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || protected(part) {
			return nil, errors.New("codextools_path_invalid")
		}
	}
	return parts, nil
}

func openDirectory(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func openFile(parent *os.File, name string, flags int) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func sameAt(parent *os.File, name string, held *os.File) error {
	var named unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	heldInfo, err := held.Stat()
	if err != nil {
		return err
	}
	id, ok := fileIdentity(heldInfo)
	if !ok || id != (identity{dev: uint64(named.Dev), ino: uint64(named.Ino)}) {
		return errors.New("identity changed")
	}
	return nil
}
func safeRegular(f *os.File) (identity, error) {
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !owned(info) {
		return identity{}, errors.New("codextools_file_invalid")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink != 1 || info.Size() < 0 || info.Size() > maxFileBytes {
		return identity{}, errors.New("codextools_file_invalid")
	}
	return identity{uint64(st.Dev), uint64(st.Ino)}, nil
}
func ownedDir(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.IsDir() && owned(info)
}
func owned(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid())
}
func fileIdentity(info os.FileInfo) (identity, bool) {
	if info == nil {
		return identity{}, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return identity{uint64(st.Dev), uint64(st.Ino)}, ok
}
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func validSHA(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == sha256.Size && strings.ToLower(value) == value
}
func protected(name string) bool {
	return strings.EqualFold(name, ".git") || strings.EqualFold(name, ".codex")
}
