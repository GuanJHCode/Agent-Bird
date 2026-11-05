//go:build !darwin

package store

import "os"

func reworkCleanupPlatform() error { return CodeError("rework_cleanup_platform_unsupported") }

func reworkCaptureNoReplace(source, quarantine *os.File, name string) error {
	return CodeError("rework_cleanup_platform_unsupported")
}
