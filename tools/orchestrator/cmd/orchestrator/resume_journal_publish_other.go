//go:build !darwin && !linux && !windows

package main

func resumeJournalPublishNoReplace(int, string, int, string) error {
	return codeError("resume_journal_publication_unsupported")
}
