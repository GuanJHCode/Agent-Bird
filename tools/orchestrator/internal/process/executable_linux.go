//go:build linux

package process

import "context"

func ExecutablePath(pid int) (string, error) { return linuxExecutablePath(context.Background(), pid) }
