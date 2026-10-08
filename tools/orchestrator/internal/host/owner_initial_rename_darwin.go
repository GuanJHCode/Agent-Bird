package host

import "golang.org/x/sys/unix"

func initialOwnerRenameNoReplace(fd int, old, next string) error {
	return unix.RenameatxNp(fd, old, fd, next, unix.RENAME_EXCL)
}
