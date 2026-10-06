package host

import (
	"context"
	"testing"
)

// The public runtime entry must reject before it creates spool state when its
// inherited control endpoint is absent.  This is the first boundary exercised
// by the CLI before it accepts any owner bootstrap.
func TestRunOriginalOwnerRejectsMissingControl(t *testing.T) {
	fixture, _, _ := ownerEnrollmentFixture(t)
	if err := RunOriginalOwner(context.Background(), OriginalOwnerConfig{Bootstrap: fixture.bootstrap}); err == nil {
		t.Fatal("runtime accepted missing inherited control endpoint")
	}
}
