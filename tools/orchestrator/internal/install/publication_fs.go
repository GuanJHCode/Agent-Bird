//go:build !windows

package install

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const installMaxFiles = 4096
const installMaxRecord = 8 << 20

// Count every byte actually copied, including an added supplied binary.
const installMaxBytes int64 = 1 << 30
const installMaxBinaryBytes int64 = 512 << 20

type publicationDirectoryIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	UID    uint32 `json:"uid"`
	Mode   uint32 `json:"mode"`
}

type publicationFileIdentity struct {
	Directory publicationDirectoryIdentity `json:"identity"`
	Links     uint64                       `json:"nlink"`
	Size      int64                        `json:"size"`
	Modified  int64                        `json:"mtime_ns"`
}

type installPayloadFile struct {
	Path   string `json:"path"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type installPayloadDirectory struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
}

type publicationTree struct {
	Files        []installPayloadFile
	FileIDs      map[string]publicationFileIdentity
	Directories  []installPayloadDirectory
	DirectoryIDs map[string]publicationDirectoryIdentity
}

type installRequest struct {
	Version         string                    `json:"version"`
	SourceRoot      string                    `json:"source_root"`
	BinaryPath      string                    `json:"binary_path"`
	DestinationRoot string                    `json:"destination_root"`
	DataRoot        string                    `json:"data_root"`
	Payload         []installPayloadFile      `json:"payload"`
	Directories     []installPayloadDirectory `json:"directories"`
	Binary          installPayloadFile        `json:"binary"`
}

func publicationDirectory(info os.FileInfo) (publicationDirectoryIdentity, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (!info.IsDir() && !info.Mode().IsRegular()) || int(st.Uid) != os.Geteuid() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return publicationDirectoryIdentity{}, errors.New("install_path_unsafe")
	}
	return publicationDirectoryIdentity{uint64(st.Dev), uint64(st.Ino), uint32(st.Uid), uint32(info.Mode().Perm())}, nil
}

func publicationFile(info os.FileInfo) (publicationFileIdentity, error) {
	dir, err := publicationDirectory(info)
	if err != nil || !info.Mode().IsRegular() {
		return publicationFileIdentity{}, errors.New("install_file_unsafe")
	}
	st := info.Sys().(*syscall.Stat_t)
	if st.Nlink != 1 || info.Size() < 0 {
		return publicationFileIdentity{}, errors.New("install_file_unsafe")
	}
	return publicationFileIdentity{dir, uint64(st.Nlink), info.Size(), info.ModTime().UnixNano()}, nil
}

func publicationRelative(name string) bool {
	return name != "" && name != "." && len(name) <= 4096 && strings.Count(name, "/") <= 64 && utf8.ValidString(name) && !strings.ContainsAny(name, "\\\x00") && !path.IsAbs(name) && path.Clean(name) == name && name != ".." && !strings.HasPrefix(name, "../")
}

func publicationOpen(parent *os.File, name string, directory bool) (*os.File, error) {
	if name != "." && (filepath.Base(name) != name || name == "..") {
		return nil, errors.New("install_component_invalid")
	}
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(int(parent.Fd()), name, flags, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func publicationOpenRoot(root string) (*os.File, error) {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), root)
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if _, err = publicationDirectory(info); err != nil {
		f.Close()
		return nil, err
	}
	if err = publicationSamePath(f, root); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func publicationSamePath(f *os.File, name string) error {
	before, e1 := f.Stat()
	after, e2 := os.Lstat(name)
	canonical, e3 := filepath.EvalSymlinks(name)
	if e1 != nil || e2 != nil || e3 != nil || canonical != name || !os.SameFile(before, after) {
		return errors.New("install_path_changed")
	}
	return nil
}

func publicationHash(parent *os.File, base string, maximum int64) (installPayloadFile, publicationFileIdentity, error) {
	f, err := publicationOpen(parent, base, false)
	if err != nil {
		return installPayloadFile{}, publicationFileIdentity{}, err
	}
	defer f.Close()
	return publicationHashOpen(parent, base, f, maximum)
}

func publicationHashOpen(parent *os.File, base string, f *os.File, maximum int64) (installPayloadFile, publicationFileIdentity, error) {
	info, err := f.Stat()
	if err != nil {
		return installPayloadFile{}, publicationFileIdentity{}, err
	}
	id, err := publicationFile(info)
	if err != nil || id.Size > maximum {
		return installPayloadFile{}, id, errors.New("install_payload_limit")
	}
	h := sha256.New()
	n, err := io.CopyBuffer(h, io.LimitReader(f, maximum+1), make([]byte, 64<<10))
	if err != nil {
		return installPayloadFile{}, id, err
	}
	after, err := f.Stat()
	if err != nil {
		return installPayloadFile{}, id, err
	}
	afterID, err := publicationFile(after)
	check, checkErr := publicationOpen(parent, base, false)
	if checkErr != nil {
		return installPayloadFile{}, id, errors.New("install_source_changed")
	}
	defer check.Close()
	current, checkErr := check.Stat()
	if err != nil || checkErr != nil || id != afterID || !os.SameFile(info, current) || n != id.Size || n > maximum {
		return installPayloadFile{}, id, errors.New("install_source_changed")
	}
	return installPayloadFile{Mode: id.Directory.Mode, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, id, nil
}

// All iteration is rooted at opened descriptors and bounded before hashing.
func publicationScan(root *os.File) (publicationTree, error) {
	tree := publicationTree{FileIDs: map[string]publicationFileIdentity{}, DirectoryIDs: map[string]publicationDirectoryIdentity{}}
	var total int64
	directoryCount := 0
	var walk func(*os.File, string) error
	walk = func(dir *os.File, prefix string) error {
		before, err := dir.Stat()
		if err != nil {
			return err
		}
		beforeID, err := publicationDirectory(before)
		if err != nil {
			return err
		}
		listing, err := publicationOpen(dir, ".", true)
		if err != nil {
			return err
		}
		defer listing.Close()
		for {
			entries, readErr := listing.ReadDir(64)
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return readErr
			}
			for _, entry := range entries {
				rel := path.Join(prefix, entry.Name())
				if !publicationRelative(rel) || entry.Type()&os.ModeSymlink != 0 {
					return errors.New("install_source_unsafe")
				}
				if entry.IsDir() {
					directoryCount++
					if directoryCount > installMaxFiles || strings.Count(rel, "/") > 64 {
						return errors.New("install_payload_limit")
					}
					child, err := publicationOpen(dir, entry.Name(), true)
					if err != nil {
						return err
					}
					info, err := child.Stat()
					var id publicationDirectoryIdentity
					if err == nil {
						id, err = publicationDirectory(info)
					}
					if err == nil {
						tree.Directories = append(tree.Directories, installPayloadDirectory{rel, id.Mode})
						tree.DirectoryIDs[rel] = id
					}
					if err == nil {
						err = walk(child, rel)
					}
					child.Close()
					if err == nil {
						current, e := publicationOpen(dir, entry.Name(), true)
						if e != nil {
							return e
						}
						after, e := current.Stat()
						current.Close()
						if e != nil || !os.SameFile(info, after) {
							return errors.New("install_directory_changed")
						}
					}
					if err != nil {
						return err
					}
					continue
				}
				if len(tree.Files) >= installMaxFiles {
					return errors.New("install_payload_limit")
				}
				remaining := installMaxBytes - total
				if rel == "manifest.json" {
					remaining = 4 << 20
				}
				file, id, err := publicationHash(dir, entry.Name(), remaining)
				if err != nil {
					return err
				}
				file.Path = rel
				if rel != "manifest.json" {
					total += file.Size
				}
				tree.Files = append(tree.Files, file)
				tree.FileIDs[rel] = id
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
		}
		after, err := dir.Stat()
		if err != nil {
			return err
		}
		afterID, err := publicationDirectory(after)
		if err != nil || afterID != beforeID {
			return errors.New("install_directory_changed")
		}
		return nil
	}
	if err := walk(root, ""); err != nil {
		return publicationTree{}, err
	}
	sort.Slice(tree.Files, func(i, j int) bool { return tree.Files[i].Path < tree.Files[j].Path })
	sort.Slice(tree.Directories, func(i, j int) bool { return tree.Directories[i].Path < tree.Directories[j].Path })
	if err := publicationCheckDirectoryChain(root, tree.DirectoryIDs); err != nil {
		return publicationTree{}, err
	}
	return tree, nil
}

// Reopen each complete parent chain, using only no-follow descriptors. A file
// inode does not prove that its parent is still the directory originally sealed.
func publicationCheckDirectoryChain(root *os.File, expected map[string]publicationDirectoryIdentity) error {
	for relative := range expected {
		current := root
		var opened []*os.File
		prefix := ""
		var err error
		for _, part := range strings.Split(relative, "/") {
			prefix = path.Join(prefix, part)
			child, e := publicationOpen(current, part, true)
			if e != nil {
				err = e
				break
			}
			opened = append(opened, child)
			info, e := child.Stat()
			if e != nil {
				err = e
				break
			}
			id, e := publicationDirectory(info)
			if e != nil || id != expected[prefix] {
				err = errors.New("install_directory_changed")
				break
			}
			current = child
		}
		for n := len(opened) - 1; n >= 0; n-- {
			opened[n].Close()
		}
		if err != nil {
			return err
		}
	}
	return nil
}

type publicationAnchors struct {
	rootPath  string
	root      *os.File
	versions  *os.File
	manifests *os.File
	intents   *os.File
}

func openPublicationAnchors(root string) (*publicationAnchors, error) {
	a := &publicationAnchors{rootPath: root}
	var err error
	a.root, err = publicationOpenRoot(root)
	if err != nil {
		return nil, err
	}
	for _, item := range []struct {
		name   string
		target **os.File
	}{{"versions", &a.versions}, {"manifests", &a.manifests}, {"install-intents", &a.intents}} {
		*item.target, err = publicationOpen(a.root, item.name, true)
		if err != nil {
			a.close()
			return nil, err
		}
	}
	if err = a.validate(); err != nil {
		a.close()
		return nil, err
	}
	return a, nil
}

func (a *publicationAnchors) close() {
	for _, f := range []*os.File{a.intents, a.manifests, a.versions, a.root} {
		if f != nil {
			f.Close()
		}
	}
}
func (a *publicationAnchors) validate() error {
	for _, item := range []struct {
		name string
		f    *os.File
	}{{"", a.root}, {"versions", a.versions}, {"manifests", a.manifests}, {"install-intents", a.intents}} {
		if err := publicationSamePath(item.f, filepath.Join(a.rootPath, item.name)); err != nil {
			return err
		}
		info, err := item.f.Stat()
		if err != nil {
			return err
		}
		id, err := publicationDirectory(info)
		if err != nil || id.Mode != 0700 {
			return errors.New("install_directory_unsafe")
		}
	}
	return nil
}

// The no-replacement syscall operates on the retained parent, then both the
// public parent path and the moved original inode are checked again.
func (a *publicationAnchors) publishStage(old, version string, expected publicationDirectoryIdentity) error {
	if err := a.validate(); err != nil {
		return err
	}
	f, err := publicationOpen(a.versions, old, true)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	id, err := publicationDirectory(info)
	if err != nil || id != expected {
		return errors.New("install_stage_changed")
	}
	if err = publicationRenameNoReplace(int(a.versions.Fd()), old, int(a.versions.Fd()), version); err != nil {
		return err
	}
	moved, err := publicationOpen(a.versions, version, true)
	if err != nil {
		return err
	}
	defer moved.Close()
	after, err := moved.Stat()
	if err != nil || !os.SameFile(info, after) {
		return errors.New("install_stage_changed")
	}
	return a.validate()
}

func publicationExists(parent *os.File, name string) (bool, error) {
	var st unix.Stat_t
	err := unix.Fstatat(int(parent.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	return err == nil, err
}

func (a *publicationAnchors) makeStage(version string) (*os.File, string, publicationDirectoryIdentity, error) {
	if err := a.validate(); err != nil {
		return nil, "", publicationDirectoryIdentity{}, err
	}
	for i := 0; i < 8; i++ {
		var nonce [6]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, "", publicationDirectoryIdentity{}, err
		}
		name := ".stage-" + version + "-" + hex.EncodeToString(nonce[:])
		if err := unix.Mkdirat(int(a.versions.Fd()), name, 0700); errors.Is(err, unix.EEXIST) {
			continue
		} else if err != nil {
			return nil, "", publicationDirectoryIdentity{}, err
		}
		f, err := publicationOpen(a.versions, name, true)
		if err != nil {
			return nil, "", publicationDirectoryIdentity{}, err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, "", publicationDirectoryIdentity{}, err
		}
		id, err := publicationDirectory(info)
		if err != nil || id.Mode != 0700 {
			f.Close()
			return nil, "", publicationDirectoryIdentity{}, errors.New("install_stage_unsafe")
		}
		if err = a.validate(); err != nil {
			f.Close()
			return nil, "", publicationDirectoryIdentity{}, err
		}
		return f, name, id, nil
	}
	return nil, "", publicationDirectoryIdentity{}, errors.New("stage_collision")
}

func publicationWriteAt(parent *os.File, name string, body []byte) (publicationFileIdentity, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return publicationFileIdentity{}, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return publicationFileIdentity{}, err
	}
	n, err := f.Write(body)
	if err == nil && n != len(body) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return publicationFileIdentity{}, err
	}
	if err = f.Sync(); err != nil {
		return publicationFileIdentity{}, err
	}
	info, err := f.Stat()
	if err != nil {
		return publicationFileIdentity{}, err
	}
	id, err := publicationFile(info)
	if err != nil {
		return publicationFileIdentity{}, err
	}
	if err = f.Close(); err != nil {
		return publicationFileIdentity{}, err
	}
	return id, nil
}

// A synced private file is published by the same exclusive primitive. A crash
// leaves either one complete record or an unbound temporary, never a partial
// record which a reader could mistake for a committed intent/index.
func (a *publicationAnchors) publishRecord(parent *os.File, name string, body []byte) error {
	if err := a.validate(); err != nil {
		return err
	}
	var nonce [6]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := ".publication-" + name + "-" + hex.EncodeToString(nonce[:])
	id, err := publicationWriteAt(parent, tmp, body)
	if err != nil {
		return err
	}
	if err = parent.Sync(); err != nil {
		return err
	}
	if err = a.validate(); err != nil {
		return err
	}
	if err = publicationRenameNoReplace(int(parent.Fd()), tmp, int(parent.Fd()), name); err != nil {
		return err
	}
	f, err := publicationOpen(parent, name, false)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	current, err := publicationFile(info)
	if err != nil || current != id {
		return errors.New("install_record_changed")
	}
	return a.validate()
}

func publicationReadAt(parent *os.File, name string, maximum int64) ([]byte, publicationFileIdentity, error) {
	f, err := publicationOpen(parent, name, false)
	if err != nil {
		return nil, publicationFileIdentity{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, publicationFileIdentity{}, err
	}
	id, err := publicationFile(info)
	if err != nil || id.Directory.Mode != 0600 || id.Size <= 0 || id.Size > maximum {
		return nil, id, errors.New("install_record_unsafe")
	}
	body, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err != nil {
		return nil, id, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, id, err
	}
	afterID, err := publicationFile(after)
	current, checkErr := publicationOpen(parent, name, false)
	if checkErr != nil {
		return nil, id, errors.New("install_record_changed")
	}
	defer current.Close()
	currentInfo, checkErr := current.Stat()
	if err != nil || checkErr != nil || afterID != id || !os.SameFile(info, currentInfo) || int64(len(body)) != id.Size {
		return nil, id, errors.New("install_record_changed")
	}
	return body, id, nil
}

func publicationSyncTree(root *os.File) error {
	listing, err := publicationOpen(root, ".", true)
	if err != nil {
		return err
	}
	defer listing.Close()
	for {
		entries, readErr := listing.ReadDir(64)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			child, err := publicationOpen(root, entry.Name(), true)
			if err != nil {
				return err
			}
			err = publicationSyncTree(child)
			child.Close()
			if err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return root.Sync()
}
