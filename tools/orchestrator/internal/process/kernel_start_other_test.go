//go:build !darwin

package process

import "testing"

func TestKernelStartIDUnsupportedOutsideDarwin(t *testing.T) {
	if got, err := KernelStartID(1); err == nil || got != "" {
		t.Fatalf("KernelStartID = %q, %v; want unsupported error", got, err)
	}
}
