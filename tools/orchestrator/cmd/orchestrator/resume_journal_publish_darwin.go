package main

import (
	"errors"
	"golang.org/x/sys/unix"
)

func resumeJournalPublishNoReplace(oldFD int, old string, newFD int, new string) error {
	err := unix.RenameatxNp(oldFD, old, newFD, new, unix.RENAME_EXCL)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.ENOTSUP) {
		return errors.Join(codeError("resume_journal_publication_unsupported"), err)
	}
	return err
}
