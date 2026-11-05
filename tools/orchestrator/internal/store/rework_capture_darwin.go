//go:build darwin

package store

import (
	"golang.org/x/sys/unix"
	"os"
)

func reworkCleanupPlatform() error { return nil }

func reworkCaptureNoReplace(source, quarantine *os.File, name string) error {
	return unix.RenameatxNp(int(source.Fd()), name, int(quarantine.Fd()), name, unix.RENAME_EXCL)
}
