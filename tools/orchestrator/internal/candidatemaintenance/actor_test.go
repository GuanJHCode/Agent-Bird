package candidatemaintenance

import (
	"bytes"
	"context"
	"testing"
)

// An invalid or absent grant is rejected before any receipt/journal/Git path
// can be opened. This is the child-side half of the pipe-before-grant fence.
func TestWorkerRejectsInvalidGrantBeforeCleanup(t *testing.T) {
	if _, err := Worker(context.Background(), bytes.NewBufferString(`{"version":1}`)); err == nil {
		t.Fatal("invalid bootstrap reached cleanup")
	}
}
