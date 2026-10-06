//go:build linux

package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxOriginalAPIsSelfBirth(t *testing.T) {
	birth, err := Birth(os.Getpid())
	if err != nil || !strings.HasPrefix(birth, "linux:") {
		t.Fatalf("Birth(self) = %q, %v; want linux kernel identity", birth, err)
	}
}

func TestLinuxOriginalAPIsSelfExecutable(t *testing.T) {
	path, err := ExecutablePath(os.Getpid())
	if err != nil || !filepath.IsAbs(path) {
		t.Fatalf("ExecutablePath(self) = %q, %v; want absolute kernel executable", path, err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("ExecutablePath(self) = %q; stat=%v mode=%v", path, err, info.Mode())
	}
}
