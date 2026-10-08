package host

import "golang.org/x/sys/unix"

func initialOwnerRenameNoReplace(fd int, old, next string) error {
	return unix.Renameat2(fd, old, fd, next, unix.RENAME_NOREPLACE)
}
