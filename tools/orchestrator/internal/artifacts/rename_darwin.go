package artifacts

import "golang.org/x/sys/unix"

func renameAbsent(dir int, from, to string) error {
	return unix.RenameatxNp(dir, from, dir, to, unix.RENAME_EXCL)
}
