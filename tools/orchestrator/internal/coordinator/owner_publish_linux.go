package coordinator

import "golang.org/x/sys/unix"

func ownerRenameNoReplace(parent int, old, new string) error {
	return unix.Renameat2(parent, old, parent, new, unix.RENAME_NOREPLACE)
}
