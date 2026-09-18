//go:build !windows

package gitops

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

// removeAnchoredTree moves every named entry into a private sibling directory,
// verifies the moved object through a descriptor, and only then unlinks it.
// A pathname replacement is therefore retained in the private directory rather
// than being mistaken for the object in expected.
func removeAnchoredTree(rootFD *os.File, parentFD *os.File, rootName string, expected map[string]FileIdentity, rootInfo os.FileInfo) error {
	if rootFD == nil || parentFD == nil || filepath.Base(rootName) != rootName || rootName == "." || rootName == ".." {
		return fail(CodeInvalidInput, "anchored remove", fmt.Errorf("invalid root handle"))
	}
	if info, err := rootFD.Stat(); err != nil || !os.SameFile(info, rootInfo) {
		return fail(CodeIdentityChanged, "anchored remove", fmt.Errorf("root handle changed"))
	}

	sinkName, sinkFD, err := makePrivateDirAt(int(parentFD.Fd()), ".g3-delete-")
	if err != nil {
		return fail(CodeUnknownObject, "anchored remove", err)
	}
	sinkKept := true
	defer func() {
		_ = sinkFD.Close()
		if !sinkKept {
			_ = unix.Unlinkat(int(parentFD.Fd()), sinkName, unix.AT_REMOVEDIR)
		}
	}()

	paths := make([]string, 0, len(expected))
	for path := range expected {
		paths = append(paths, path)
	}
	sortRemovalPaths(paths)
	for i, path := range paths {
		parent, base, closeParent, err := openRelativeParent(rootFD, path)
		if err != nil {
			return fail(CodeUnknownObject, "anchored remove", err)
		}
		destination := fmt.Sprintf("entry-%08d", i)
		err = moveVerifyUnlink(parent, base, sinkFD, destination, expected[path])
		closeParent()
		if err != nil {
			return err
		}
	}

	if info, err := rootFD.Stat(); err != nil || !os.SameFile(info, rootInfo) {
		return fail(CodeIdentityChanged, "anchored remove", fmt.Errorf("root handle changed"))
	}
	if err := unix.Renameat(int(parentFD.Fd()), rootName, int(sinkFD.Fd()), "root"); err != nil {
		return fail(CodeUnknownObject, "anchored remove root", err)
	}
	movedRoot, err := openAtFile(sinkFD, "root", true)
	if err != nil {
		return fail(CodeUnknownObject, "anchored remove root", err)
	}
	movedInfo, statErr := movedRoot.Stat()
	_ = movedRoot.Close()
	if statErr != nil || !os.SameFile(movedInfo, rootInfo) {
		return fail(CodeIdentityChanged, "anchored remove root", fmt.Errorf("moved root identity changed"))
	}
	if err := unix.Unlinkat(int(sinkFD.Fd()), "root", unix.AT_REMOVEDIR); err != nil {
		return fail(CodeUnknownObject, "anchored remove root", err)
	}
	sinkKept = false
	return nil
}

func sortRemovalPaths(paths []string) {
	sort.Slice(paths, func(i, j int) bool {
		left, right := strings.Count(paths[i], "/"), strings.Count(paths[j], "/")
		if left == right {
			return paths[i] > paths[j]
		}
		return left > right
	})
}

func openRelativeParent(root *os.File, relative string) (*os.File, string, func(), error) {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return nil, "", func() {}, errors.New("relative path escapes root")
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
		next, err := openAtFile(current, part, true)
		if err != nil {
			closeOpened()
			return nil, "", func() {}, err
		}
		opened = append(opened, next)
		current = next
	}
	return current, parts[len(parts)-1], closeOpened, nil
}

func moveVerifyUnlink(sourceParent *os.File, sourceName string, sink *os.File, sinkName string, expected FileIdentity) error {
	if err := unix.Renameat(int(sourceParent.Fd()), sourceName, int(sink.Fd()), sinkName); err != nil {
		return fail(CodeUnknownObject, "anchored rename", err)
	}
	isDir := os.FileMode(expected.Mode).IsDir()
	moved, err := openAtFile(sink, sinkName, isDir)
	if err != nil {
		return fail(CodeUnknownObject, "anchored verify", err)
	}
	info, statErr := moved.Stat()
	_ = moved.Close()
	if statErr != nil {
		return fail(CodeUnknownObject, "anchored verify", statErr)
	}
	actual, err := identityFromInfo(info)
	if err != nil || !sameRemovalIdentity(actual, expected) {
		return fail(CodeIdentityChanged, "anchored verify", fmt.Errorf("moved entry identity changed"))
	}
	flags := 0
	if isDir {
		flags = unix.AT_REMOVEDIR
	}
	if err := unix.Unlinkat(int(sink.Fd()), sinkName, flags); err != nil {
		return fail(CodeUnknownObject, "anchored unlink", err)
	}
	return nil
}

func sameRemovalIdentity(actual, expected FileIdentity) bool {
	if os.FileMode(expected.Mode).IsDir() {
		return actual.Device == expected.Device && actual.Inode == expected.Inode && actual.Mode == expected.Mode
	}
	return sameIdentity(actual, expected)
}

func openAtFile(parent *os.File, name string, directory bool) (*os.File, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return nil, errors.New("invalid relative component")
	}
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(int(parent.Fd()), name, flags, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func makePrivateDirAt(parentFD int, prefix string) (string, *os.File, error) {
	for i := 0; i < 16; i++ {
		name, err := randomComponent(prefix)
		if err != nil {
			return "", nil, err
		}
		if err := unix.Mkdirat(parentFD, name, 0o700); err != nil {
			if errors.Is(err, unix.EEXIST) {
				continue
			}
			return "", nil, err
		}
		fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return name, nil, err
		}
		file := os.NewFile(uintptr(fd), name)
		info, err := file.Stat()
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			_ = file.Close()
			return name, nil, errors.New("private directory verification failed")
		}
		return name, file, nil
	}
	return "", nil, errors.New("private directory collision")
}

func quarantineAt(parent *os.File, sourceName, prefix string) (string, error) {
	if filepath.Base(sourceName) != sourceName || sourceName == "." || sourceName == ".." {
		return "", errors.New("invalid source component")
	}
	for i := 0; i < 16; i++ {
		name, err := randomComponent(prefix)
		if err != nil {
			return "", err
		}
		// Reserve the destination and then remove that empty reservation through
		// the same anchored parent. Renameat will either move one source object
		// to this unpredictable name or fail; it never replaces an existing path.
		if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
			if errors.Is(err, unix.EEXIST) {
				continue
			}
			return "", err
		}
		if err := unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR); err != nil {
			return "", err
		}
		if err := unix.Renameat(int(parent.Fd()), sourceName, int(parent.Fd()), name); err != nil {
			return "", err
		}
		return name, nil
	}
	return "", errors.New("quarantine collision")
}

func randomComponent(prefix string) (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value[:]), nil
}
