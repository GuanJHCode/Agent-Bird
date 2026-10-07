package main

import (
	"errors"
	"golang.org/x/sys/unix"
)

func resumeJournalPublishNoReplace(oldFD int, old string, newFD int, new string) error {
	err := unix.Renameat2(oldFD, old, newFD, new, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) {
		return errors.Join(codeError("resume_journal_publication_unsupported"), err)
	}
	return err
}
