//go:build !windows

package install

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type directoryFileIdentity struct {
	device, inode uint64
	mode          os.FileMode
}

func removeOwnedTreeAnchored(root string, value manifest, manifestHash string, allowMissing bool) ([]string, error) {
	parentPath := filepath.Dir(root)
	parent, err := os.Open(parentPath)
	if err != nil {
		return []string{root}, err
	}
	defer parent.Close()
	rootName := filepath.Base(root)
	quarantineName := ".uninstall-root-" + value.Version
	deleteName := ".uninstall-delete-" + value.Version

	rootExists := existsAt(parent, rootName)
	quarantineExists := existsAt(parent, quarantineName)
	if rootExists == quarantineExists {
		return []string{root, filepath.Join(parentPath, quarantineName)}, errors.New("uninstall_root_ambiguous")
	}
	activeName := quarantineName
	if rootExists {
		rootFD, openErr := openDirAt(parent, rootName)
		if openErr != nil {
			return []string{root}, openErr
		}
		before, statErr := rootFD.Stat()
		if statErr != nil {
			_ = rootFD.Close()
			return []string{root}, statErr
		}
		if err := unix.Renameat(int(parent.Fd()), rootName, int(parent.Fd()), quarantineName); err != nil {
			_ = rootFD.Close()
			return []string{root}, err
		}
		moved, openErr := openDirAt(parent, quarantineName)
		if openErr != nil {
			_ = rootFD.Close()
			return []string{filepath.Join(parentPath, quarantineName)}, openErr
		}
		after, statErr := moved.Stat()
		_ = moved.Close()
		_ = rootFD.Close()
		if statErr != nil || !os.SameFile(before, after) {
			return []string{filepath.Join(parentPath, quarantineName)}, errors.New("uninstall_root_identity_changed")
		}
	}

	rootFD, err := openDirAt(parent, activeName)
	if err != nil {
		return []string{filepath.Join(parentPath, activeName)}, err
	}
	defer rootFD.Close()

	deleteFD, created, err := openOrCreatePrivateDirAt(parent, deleteName)
	if err != nil {
		return []string{filepath.Join(parentPath, deleteName)}, err
	}
	deleteKept := true
	defer func() {
		_ = deleteFD.Close()
		if !deleteKept {
			_ = unix.Unlinkat(int(parent.Fd()), deleteName, unix.AT_REMOVEDIR)
		}
	}()
	if !created {
		names, readErr := deleteFD.Readdirnames(1)
		if readErr != io.EOF || len(names) != 0 {
			return []string{filepath.Join(parentPath, deleteName)}, errors.New("uninstall_quarantine_retained")
		}
	}

	directories, err := captureKnownDirectories(rootFD, value)
	if err != nil {
		return []string{filepath.Join(parentPath, activeName)}, err
	}
	files := append([]manifestFile(nil), value.Files...)
	manifestInfo, err := statRelative(rootFD, "manifest.json")
	if err == nil {
		stat := manifestInfo.Sys().(*syscall.Stat_t)
		files = append(files, manifestFile{Path: "manifest.json", SHA256: manifestHash, Mode: uint32(manifestInfo.Mode().Perm()), Device: uint64(stat.Dev), Inode: uint64(stat.Ino), Size: manifestInfo.Size(), ModUnixNano: manifestInfo.ModTime().UnixNano()})
	} else if !allowMissing || !errors.Is(err, os.ErrNotExist) {
		return []string{filepath.Join(parentPath, activeName, "manifest.json")}, err
	}

	for index, file := range files {
		movedName := fmt.Sprintf("file-%08d", index)
		missing, err := moveVerifiedFile(rootFD, file, deleteFD, movedName)
		if missing && allowMissing {
			continue
		}
		if err != nil {
			return []string{filepath.Join(parentPath, deleteName, movedName)}, err
		}
	}

	directoryPaths := make([]string, 0, len(directories))
	for path := range directories {
		directoryPaths = append(directoryPaths, path)
	}
	sort.Slice(directoryPaths, func(i, j int) bool {
		left, right := strings.Count(directoryPaths[i], "/"), strings.Count(directoryPaths[j], "/")
		if left == right {
			return directoryPaths[i] > directoryPaths[j]
		}
		return left > right
	})
	for index, path := range directoryPaths {
		movedName := fmt.Sprintf("dir-%08d", index)
		missing, err := moveVerifiedDirectory(rootFD, path, directories[path], deleteFD, movedName)
		if missing && allowMissing {
			continue
		}
		if err != nil {
			return []string{filepath.Join(parentPath, deleteName, movedName)}, err
		}
	}

	rootInfo, err := rootFD.Stat()
	if err != nil {
		return []string{filepath.Join(parentPath, activeName)}, err
	}
	if err := unix.Renameat(int(parent.Fd()), activeName, int(deleteFD.Fd()), "root"); err != nil {
		return []string{filepath.Join(parentPath, activeName)}, err
	}
	movedRoot, err := openDirAt(deleteFD, "root")
	if err != nil {
		return []string{filepath.Join(parentPath, deleteName, "root")}, err
	}
	movedInfo, statErr := movedRoot.Stat()
	_ = movedRoot.Close()
	if statErr != nil || !os.SameFile(rootInfo, movedInfo) {
		return []string{filepath.Join(parentPath, deleteName, "root")}, errors.New("uninstall_root_identity_changed")
	}
	if err := unix.Unlinkat(int(deleteFD.Fd()), "root", unix.AT_REMOVEDIR); err != nil {
		return []string{filepath.Join(parentPath, deleteName, "root")}, err
	}
	deleteKept = false
	return nil, nil
}

func moveVerifiedFile(root *os.File, file manifestFile, sink *os.File, sinkName string) (bool, error) {
	parent, base, closeParent, err := openRelativeParent(root, file.Path)
	if err != nil {
		return errors.Is(err, os.ErrNotExist), err
	}
	defer closeParent()
	source, err := openFileAt(parent, base)
	if err != nil {
		return errors.Is(err, os.ErrNotExist), err
	}
	info, err := source.Stat()
	if err != nil || !matchesManifestIdentity(info, file) {
		_ = source.Close()
		return false, errors.New("installed_file_identity_changed")
	}
	digest, err := hashOpenFile(source)
	_ = source.Close()
	if err != nil || digest != file.SHA256 {
		return false, ErrChangedFiles
	}
	if err := unix.Renameat(int(parent.Fd()), base, int(sink.Fd()), sinkName); err != nil {
		return false, err
	}
	moved, err := openFileAt(sink, sinkName)
	if err != nil {
		return false, err
	}
	movedInfo, statErr := moved.Stat()
	movedDigest, hashErr := hashOpenFile(moved)
	_ = moved.Close()
	if statErr != nil || hashErr != nil || !matchesManifestIdentity(movedInfo, file) || movedDigest != file.SHA256 {
		return false, errors.New("installed_file_identity_changed")
	}
	return false, unix.Unlinkat(int(sink.Fd()), sinkName, 0)
}

func moveVerifiedDirectory(root *os.File, path string, expected directoryFileIdentity, sink *os.File, sinkName string) (bool, error) {
	parent, base, closeParent, err := openRelativeParent(root, path)
	if err != nil {
		return errors.Is(err, os.ErrNotExist), err
	}
	defer closeParent()
	if err := unix.Renameat(int(parent.Fd()), base, int(sink.Fd()), sinkName); err != nil {
		return errors.Is(err, unix.ENOENT), err
	}
	moved, err := openDirAt(sink, sinkName)
	if err != nil {
		return false, err
	}
	info, statErr := moved.Stat()
	_ = moved.Close()
	if statErr != nil || !sameDirectoryFileIdentity(info, expected) {
		return false, errors.New("installed_directory_identity_changed")
	}
	return false, unix.Unlinkat(int(sink.Fd()), sinkName, unix.AT_REMOVEDIR)
}

func captureKnownDirectories(root *os.File, value manifest) (map[string]directoryFileIdentity, error) {
	known := map[string]bool{}
	for _, file := range append(append([]manifestFile(nil), value.Files...), manifestFile{Path: "manifest.json"}) {
		current := filepath.ToSlash(filepath.Dir(filepath.FromSlash(file.Path)))
		for current != "." && current != "" {
			known[current] = true
			current = filepath.ToSlash(filepath.Dir(filepath.FromSlash(current)))
		}
	}
	result := map[string]directoryFileIdentity{}
	for path := range known {
		info, err := statRelative(root, path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() {
			return nil, errors.New("installed_directory_unsafe")
		}
		stat := info.Sys().(*syscall.Stat_t)
		result[path] = directoryFileIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino), mode: info.Mode()}
	}
	return result, nil
}

func statRelative(root *os.File, path string) (os.FileInfo, error) {
	parent, base, closeParent, err := openRelativeParent(root, path)
	if err != nil {
		return nil, err
	}
	defer closeParent()
	file, err := openFileAt(parent, base)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.Stat()
}

func openRelativeParent(root *os.File, relative string) (*os.File, string, func(), error) {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return nil, "", func() {}, errors.New("owned_path_invalid")
	}
	parts := strings.Split(clean, string(os.PathSeparator))
	current := root
	opened := []*os.File{}
	closeOpened := func() {
		for i := len(opened) - 1; i >= 0; i-- {
			_ = opened[i].Close()
		}
	}
	for _, part := range parts[:len(parts)-1] {
		next, err := openDirAt(current, part)
		if err != nil {
			closeOpened()
			return nil, "", func() {}, err
		}
		opened = append(opened, next)
		current = next
	}
	return current, parts[len(parts)-1], closeOpened, nil
}

func openDirAt(parent *os.File, name string) (*os.File, error) {
	return openAt(parent, name, unix.O_DIRECTORY)
}

func openFileAt(parent *os.File, name string) (*os.File, error) {
	return openAt(parent, name, 0)
}

func openAt(parent *os.File, name string, extra int) (*os.File, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return nil, errors.New("owned_component_invalid")
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|extra, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func openOrCreatePrivateDirAt(parent *os.File, name string) (*os.File, bool, error) {
	created := false
	if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err == nil {
		created = true
	} else if !errors.Is(err, unix.EEXIST) {
		return nil, false, err
	}
	file, err := openDirAt(parent, name)
	if err != nil {
		return nil, false, err
	}
	info, err := file.Stat()
	if err != nil || info.Mode().Perm() != 0o700 {
		_ = file.Close()
		return nil, false, errors.New("uninstall_quarantine_unsafe")
	}
	return file, created, nil
}

func existsAt(parent *os.File, name string) bool {
	file, err := openAt(parent, name, 0)
	if err != nil {
		return false
	}
	_ = file.Close()
	return true
}

func sameDirectoryFileIdentity(info os.FileInfo, expected directoryFileIdentity) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.IsDir() && uint64(stat.Dev) == expected.device && uint64(stat.Ino) == expected.inode && info.Mode() == expected.mode
}

func hashOpenFile(file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}
