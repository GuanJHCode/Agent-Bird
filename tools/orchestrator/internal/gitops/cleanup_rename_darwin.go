package gitops

import (
	"golang.org/x/sys/unix"
	"os"
)

func cleanupRenameExclusive(source *os.File, old string, target *os.File, name string) error {
	return unix.RenameatxNp(int(source.Fd()), old, int(target.Fd()), name, unix.RENAME_EXCL)
}
