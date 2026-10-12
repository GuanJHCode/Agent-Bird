//go:build !darwin && !linux && !windows

package gitops

import (
	"errors"
	"os"
)

func cleanupRenameExclusive(source *os.File, old string, target *os.File, name string) error {
	return errors.New("exclusive cleanup rename unsupported on this platform")
}
