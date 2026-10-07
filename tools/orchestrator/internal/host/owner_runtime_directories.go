package host

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"golang.org/x/sys/unix"
)

// Keep the original directory identities through Host/ledger initialization.
// The recovery ledger subsequently holds its own FD/lock authority. This is an
// initialization guard, not a claim that every legacy Host path operation has
// been converted to descriptor-relative I/O.
type ownerRuntimeDirectories struct {
	paths []string
	files []*os.File
}

var errOwnerRuntimeDirectory = errors.New("owner_runtime_directory_rejected")

func openOwnerRuntimeDirectories(b contract.OwnerRuntimeBootstrap) (result *ownerRuntimeDirectories, resultErr error) {
	if b.Validate() != nil {
		return nil, errOwnerRuntimeDirectory
	}
	state, err := os.OpenFile(b.StateDir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errOwnerRuntimeDirectory
	}
	dirs := &ownerRuntimeDirectories{paths: []string{b.StateDir}, files: []*os.File{state}}
	defer func() {
		if resultErr != nil {
			dirs.close()
		}
	}()
	if err = dirs.verify(); err != nil {
		return nil, err
	}
	for _, name := range []string{"host-spool", b.Prepared.Intent.Scope.LaunchID} {
		parent := dirs.files[len(dirs.files)-1]
		// A failed/uncertain creation never authorizes Host or ledger writes.
		created := unix.Mkdirat(int(parent.Fd()), name, 0700)
		if created != nil && !errors.Is(created, unix.EEXIST) {
			return nil, created
		}
		child, err := recoveryFileAt(parent, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			return nil, errOwnerRuntimeDirectory
		}
		dirs.files = append(dirs.files, child)
		dirs.paths = append(dirs.paths, filepath.Join(dirs.paths[len(dirs.paths)-1], name))
		if err = dirs.verify(); err != nil {
			return nil, err
		}
		if created == nil {
			if err = parent.Sync(); err != nil {
				return nil, err
			}
		}
	}
	if err = dirs.verify(); err != nil {
		return nil, err
	}
	return dirs, nil
}

func (d *ownerRuntimeDirectories) verify() error {
	if d == nil || len(d.files) == 0 || len(d.paths) != len(d.files) {
		return errOwnerRuntimeDirectory
	}
	resolved, err := filepath.EvalSymlinks(d.paths[0])
	if err != nil || resolved != d.paths[0] {
		return errOwnerRuntimeDirectory
	}
	for index, file := range d.files {
		if file == nil {
			return errOwnerRuntimeDirectory
		}
		held, err := file.Stat()
		if err != nil || !privateRecoveryInfo(held, true) {
			return errOwnerRuntimeDirectory
		}
		current, err := os.Lstat(d.paths[index])
		if err != nil || !privateRecoveryInfo(current, true) || !os.SameFile(held, current) {
			return errOwnerRuntimeDirectory
		}
		if index > 0 {
			linked, err := recoveryFileAt(d.files[index-1], filepath.Base(d.paths[index]), unix.O_RDONLY|unix.O_DIRECTORY, 0)
			if err != nil {
				return errOwnerRuntimeDirectory
			}
			info, err := linked.Stat()
			_ = linked.Close()
			if err != nil || !privateRecoveryInfo(info, true) || !os.SameFile(held, info) {
				return errOwnerRuntimeDirectory
			}
		}
	}
	return nil
}

func (d *ownerRuntimeDirectories) close() {
	for index := len(d.files) - 1; index >= 0; index-- {
		if d.files[index] != nil {
			_ = d.files[index].Close()
			d.files[index] = nil
		}
	}
}
