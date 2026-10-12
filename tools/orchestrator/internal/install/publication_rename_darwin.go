package install

import (
	"errors"
	"golang.org/x/sys/unix"
)

func publicationRenameNoReplace(oldFD int, old string, newFD int, new string) error {
	err := unix.RenameatxNp(oldFD, old, newFD, new, unix.RENAME_EXCL)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.ENOTSUP) {
		return errors.Join(errors.New("install_publication_unsupported"), err)
	}
	return err
}
