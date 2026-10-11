package gitops

import (
	"context"
	"testing"
	"time"
)

func TestOpenFilterTargetTransitionRejectsDetachedPair(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	transition, err := OpenFilterTargetTransition(ctx, nil)
	if transition != nil || !IsCode(err, CodeReviewMismatch) {
		t.Fatalf("transition=%v err=%v", transition, err)
	}
}
