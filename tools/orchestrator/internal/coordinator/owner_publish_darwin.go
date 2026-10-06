package coordinator

import "golang.org/x/sys/unix"

func ownerRenameNoReplace(parent int, old, new string) error {
	return unix.RenameatxNp(parent, old, parent, new, unix.RENAME_EXCL)
}
