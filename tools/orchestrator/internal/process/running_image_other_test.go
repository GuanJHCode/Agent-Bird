//go:build !darwin

package process

import (
	"context"
	"testing"
)

func TestVerifyRunningExecutableUnsupportedOutsideDarwin(t *testing.T) {
	if err := VerifyRunningExecutable(context.Background(), Identity{}, "/bin/true", "digest"); err == nil {
		t.Fatal("accepted unsupported platform")
	}
}
